# headroom-go vs upstream headroom — parity review, 2026-08-25

Upstream read at `headroomlabs-ai/headroom@6262c28` (the repo formerly at
`chopratejas/headroom`), via `gh api repos/.../tarball/HEAD`.

Baseline: `origin/main` at 760124b, 857 tests green in 27 packages.

## 1. The subscription question — we did not over-read upstream

The recorded worry was that we compress on all auth modes while upstream gates
on PAYG. Reading the call sites rather than `compression_policy.py` settles it.

Upstream's own docstring on `compress_anthropic_request`
(`crates/headroom-proxy/src/compression/live_zone_anthropic.rs:140`):

> `auth_mode`: F1's `RequestAuthMode` classification of the inbound request.
> Gates every Phase E byte-mutating pass — PR-E1 (tool-array sort), PR-E2
> (JSON Schema key sort), and PR-E3 (`cache_control` auto-placement) — on
> `Payg` only. OAuth and Subscription modes pass through byte-equal because
> mutating their bytes risks looking like cache-evasion to the upstream. **The
> live-zone dispatcher itself still runs on every mode in PR-B/C; the
> auth-mode gate is local to Phase E.**

The Rust live-zone entry point takes the mode and discards it —
`compress_anthropic_live_zone_with_ccr(_auth_mode: AuthMode, ...)`
(`live_zone.rs:646`, `:1877`, `:2334`) — and upstream has a test named
`auth_mode_does_not_affect_b3_outcome_for_short_input` (`live_zone.rs:1612`).

So upstream **does** mutate subscription request bodies. It mutates them in the
live zone only. Phase E is the sole PAYG-gated byte path.

### Where that leaves us

headroom-go has no Phase E. `internal/cachecontrol` is read-only: it computes a
frozen floor and warns on TTL ordering. It never places a marker, never sorts
`tools[]`, never sorts schema keys. `grep` over the tree confirms no field of
`CompressionPolicy` is read outside `internal/policy`.

| | upstream PAYG | upstream Subscription | headroom-go, every mode |
|---|---|---|---|
| Live-zone dispatcher | runs | runs | runs |
| Phase E: sort `tools[]` | runs | skipped | not ported |
| Phase E: sort schema keys | runs | skipped | not ported |
| Phase E: auto-place `cache_control` | runs | skipped | not ported |
| Bytes outside the live zone | mutated | untouched | untouched |

We are byte-for-byte equivalent to upstream Subscription on every mode, and
strictly **more** conservative than upstream PAYG.

### Recommendation: keep as-is; fix the spec, not the code

1. Gating `policy.ForMode` to PAYG would make us more conservative than
   upstream, not equal to it. It would disable the only compression we do, on
   the only client that matters here — Claude Code sends `claude-cli/`, which
   classifies as Subscription. The proxy would become a no-op for its main
   user.
2. Upstream's stated reason for the Phase E gate is that mutating bytes
   *outside the live zone* looks like cache-evasion. We never do that. The
   risk the gate exists to manage does not apply to our byte path.
3. The `[1m]` sanitizer aside (see §3 below), we have no code that can touch a
   cache-hot byte, so the gate has nothing to gate.

**Spec §7 is the thing that is wrong.** It says "compress request body via
live-zone dispatcher (PAYG only; Decision A)". That sentence describes neither
upstream nor us. It should read: the live-zone dispatcher runs on every auth
mode; the PAYG gate applies to Phase E normalization passes, which are not
ported.

One residual, unchanged from before: compressing a subscription request means
Anthropic sees `<<ccr:HASH>>` in the prompt. That is a product risk, not a
parity risk, and upstream carries it identically.

## 2. The six recorded divergences, re-checked

| # | Divergence | Still holds? |
|---|---|---|
| 1 | Compress on all auth modes | **Yes, and it is correct.** See §1. Upstream matches. |
| 2 | 5 `CompressionPolicy` fields plumbed, none enforced | **Yes.** Upstream is identical: `volatile_token_threshold` and `max_lossy_ratio` appear only in a `tracing::debug!` line (`proxy.rs:481-482`). `toin_read_only` and `cache_aligner_enabled` are read in Python only, by transforms we did not port. Verified by mutation: swapping the PAYG and Subscription rows in `ForMode` fails **only** `internal/policy`'s own table test. Nothing downstream moves. |
| 3 | `gjson.Result.Index` vs `bytes_offset_of` | **Yes.** Upstream's `bytes_offset_of` (`live_zone.rs:1229`) is raw pointer arithmetic on `&str` because Rust offers nothing better. Porting it to Go would be strictly worse. |
| 4 | No sjson / no chi / no SSE framer; `httputil.ReverseProxy` is the hop | **Yes.** `go.sum` carries neither. Verified by mutation: neutering the `x-headroom-*` strip fails `TestForwardHeaderHandling`; adding **any** `ModifyResponse` fails `TestLiveFixtureErrorEnvelopeIsForwardedVerbatim`. "Responses are never compressed" is structural, and now has a test that enforces it against real captured bytes. |
| 5 | 3-field `wrap` table vs upstream's `wrap.py` | **Yes.** Upstream's `headroom/cli/wrap.py` is 8277 lines (grown from the recorded 7974). Ours is a two-row map. |
| 6 | Decision B — no byte-parity | **Yes.** No `%g` emulator, no banker's rounding, no BUG#1 percentile string. |

Nothing in the six needs revisiting. Divergence 1's open risk is now closed:
it was an over-read of `compression_policy.py`, and the call sites say
otherwise.

## 3. Silently dropped, not deliberately cut

The §10 parity checklist is the map. These rows are marked **v0.2** — the
cycle the project considers complete — and do not exist in the tree. None is
tracked in beads.

| §10 row | Status | Why it matters |
|---|---|---|
| `cachestab` E3/E4/E5/E6 | **package absent** | E5 is upstream's volatile-content detector and E6 its cache-bust drift detector (`proxy.rs:690-760`). Both are read-only observability over the cache hot zone. They are how an operator sees a silent cache bust. This is the cache-stability thesis of the product, unbuilt. |
| CLI `perf` | **absent** | No `~/.headroom/logs/proxy.log` parser, and the proxy writes no PERF line to parse. |
| CLI `learn` | **absent** | No `classify_error`, no CLAUDE.md writer. |
| `memory` (cross-agent sync) | **absent** | No package, no SQLite FTS5 table. |
| Proxy `x-request-id` + `headroom-upstream-request-id` injection | **absent** | Spec §7 lists it in `forward_http`. Anthropic's own `Request-Id` does survive the hop (fixture-verified), so this is a nicety, not a hole. |

`SearchOffload (registered)` and `SmartCrusher byte-parity` are marked **skip**
in §10 and are correctly absent. Everything marked **v0.1** is present.

### One upstream behaviour worth a decision

Upstream strips a trailing `[1m]` context-tier suffix from `model` before
dispatch (`compression/mod.rs:113`, PR-2027), because the Anthropic API
rejects the suffix. We do not. This is not a silent drop — we never ported the
CLI feature that appends `[1m]` — but any client that sends such a model ID
through `headroom proxy` gets a 400 that upstream would have absorbed.

Note the cost of upstream's version: it re-serialises the whole body with
`serde_json::to_vec`, disturbing exactly the whitespace and key order the rest
of the architecture protects. Porting it would need byte-range surgery on the
`model` value instead, and it mutates a cache-hot byte either way. Decision
required before building.

## 4. Defects found by real-API contact

`internal/proxy/live_test.go` (build tag `liveapi`) drives the real
`https://api.anthropic.com`. The unauthenticated test needs no credential and
runs today; the four credentialed ones need a PAYG key.

The first real run found three defects. One is fixed here.

### A. A rejected block left an orphan CCR entry — **fixed**

`compressBlock` called `opts.Router.Compress(text, ctx, opts.Store)` and only
then applied the I5 token gate. Every compressor and offload
(`log_compressor.go:183`, `search_compressor.go:152`, `diff_compressor.go:225`,
`json_offload.go:91`, `diff_noise.go:142`, `crusher.go:317`) writes the
original inside `Compress`, unconditionally. So a rejected block left its
original in the store forever: an entry no marker on the wire ever names.

Two comments asserted the opposite and were both false —
`injectCCRMarker`'s "the caller stores the original only after the I5 gate
accepts, so a rejected block leaves no orphan entry", and `Dispatch`'s "Every
emitted marker therefore resolves."

`TestDispatchRejectedBlockLeavesNoOrphanEntry` was green throughout. It
asserted `store.Get(ccr.ComputeKey(...))` — the **BLAKE3** key. The orphan is
written under the **MD5** key (`ComputeKeyMD5`, used by every compressor for
upstream parity). A hash-specific assertion cannot see it. This is a third
instance of the failure mode already recorded twice in project memory.

Fix: `stagingStore` buffers the router's writes and replays them into the real
store only once the gate accepts. One type, ~35 lines, in `livezone`. No
compressor changed. Insertion order is replayed, so I4 holds.

Test now asserts `store.Len() == 0`. Reverting the fix turns it red.

### B. Every accepted block stores its original twice

One 25 KB block produced two store entries: `ee8571fa…` (MD5, written by the
log compressor) and `e01a767c…` (BLAKE3, written by the dispatcher), same
payload. The SQLite CCR store grows at 2x for every live-zone offload.

### C. Two retrieval markers, in two formats, in one block

The bytes sent upstream carry both of these:

```
[301 lines compressed to 4. Retrieve more: hash=ee8571fa383056ea6d6967b8]
<<ccr:e01a767c6b2ed7002cc40b10>>
```

Only `<<ccr:HASH>>` is the documented contract that `headroom_retrieve` and
`POST /v1/retrieve` implement. The compressor's own note is upstream-parity
output, it costs tokens against the I5 gate, and the model cannot act on it.

B and C are the same decision: the compressors' inline markers are why the MD5
copy must exist. Suppressing them on the live-zone path removes both the
duplicate storage and the dead tokens, at the cost of diverging from upstream
compressor output — which has its own tests. Not changed here.

`TestDispatchEveryHashInTheBodyResolves` now parses **every** hash the body
carries, in either format, and asserts each resolves. A new marker format
cannot be added without its store write.

## 5. What the real hop actually did

Against `api.anthropic.com`, a 25674-byte request, `User-Agent: claude-cli/`
(Subscription):

- 25107 bytes saved, 6307 → 104 tokens.
- Anthropic's real error envelope came back verbatim, with `Request-Id`,
  `X-Should-Retry`, `Cf-Ray` and the CSP/HSTS headers intact.
- headroom's own `X-Headroom-*` headers reached the client and did not go
  upstream.
- No `<<ccr:>>` marker appeared in any response byte.

Captured to `internal/proxy/testdata/live/`. `livefixture_test.go` replays
those bytes in the ordinary suite — no tag, no network, no credential.

## 6. Shared risk, not a divergence

`defaultListen` is `0.0.0.0:8787`. Upstream's default is the same
(`config.rs:280`). It binds every interface, so anyone on the LAN can use the
proxy as a relay and probe `POST /v1/retrieve`. `127.0.0.1:8787` would be the
safer default for a developer tool. Matching upstream here is a choice, not an
oversight, but it is worth making deliberately.
