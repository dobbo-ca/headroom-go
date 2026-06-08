# headroom-go — Core Design Spec

- **Date:** 2026-06-08
- **Status:** Approved (design + key forks decided); pending final spec review → writing-plans
- **Module:** `github.com/dobbo-ca/headroom-go`
- **License:** Apache-2.0 (matches upstream), with `NOTICE` attribution to `chopratejas/headroom`
- **House style:** mirrors sibling project `graphify-go` (`cmd/` + `internal/`, `skills/`, `GOALS.md`, beads, GitHub Actions CI + Uplift release + Homebrew tap). cgo + tree-sitter already in use in the workspace.

---

## 1. Context

Clean-room Go port of [`chopratejas/headroom`](https://github.com/chopratejas/headroom) — an LLM context-compression layer that compresses tool outputs, logs, diffs, search results, and RAG chunks before they reach the model, cutting tokens 60–95% while preserving answers.

This is **one of four sub-projects** (each gets its own spec → plan → implementation cycle):

| # | Sub-project | Lang | Repo | This spec? |
|---|---|---|---|---|
| **1** | **headroom-go core** | Go | `dobbo-ca/headroom-go` | **YES** |
| 2 | kompress-go runtime (ONNX dual-head inference) | Go | `dobbo-ca/kompress-go` | later |
| 3 | kompress training (LLMLingua-2 distillation on our traces) | Python | `dobbo-ca/kompress-go` | later |
| 4 | rtk + headroom + graphify token-reduction guide | md | `dobbo-ca/docs/` | later |

The ML prose compressor (`Kompress-base` = a ModernBERT dual-head token classifier, Apache-2.0) is **not** in core. Core defines a `Compressor`/`OffloadTransform` seam and ships a zero-dep heuristic default; the ONNX impl and our own trained model drop in later via that seam (sub-projects 2/3) with zero core changes.

### Confirmed scope decisions

- **Deployment modes:** proxy (drop-in Anthropic/OpenAI-compatible) + MCP server + CLI wrapper (`headroom wrap claude`). No embeddable-library-as-product; no Python/TS framework integrations (LangChain/Vercel/LiteLLM/Agno) — they are meaningless in Go and the Go-native equivalent is the proxy + MCP + wrapper.
- **Prose:** `Compressor` interface + heuristic default in core; ML → kompress-go.
- **Decision A — proxy compresses in v0.2:** the proxy compresses **request bodies** via the live-zone dispatcher; **responses stream verbatim, never compressed.** One step ahead of upstream's locked-down `main` but exactly along its intended rails — and it is what delivers the token-reduction goal.
- **Decision B — drop byte-parity:** clean-room Go talking to its own CCR store needs no cross-language byte-equality with Python/Rust headroom. We do **not** replicate upstream's deliberate Python float-formatting bugs (`%g` emulator, banker's rounding, the "BUG#1" percentile string). Produce clear strategy strings; document the divergence.
- **Decision C — phased delivery:** v0.1 = compression engine + MCP (usable token-saver fast); v0.2 = proxy + cache-stabilization + cross-agent memory + CLI extras (perf/learn/wrap). See §8.

### Decisions taken inline (clearly-right; not open questions)

- **Ordered JSON** is load-bearing (schema ordering, CSV output, marker placement all depend on insertion order): `github.com/iancoleman/orderedmap` for SmartCrusher; `github.com/tidwall/gjson` + `sjson` for surgical walk/inject paths. `map[string]any` is banned on compression paths.
- **Tokenizer** ships the estimator (round-half-up, rune-based) + `github.com/pkoukk/tiktoken-go` with `tiktoken-go-loader` (embedded/offline vocab — deterministic binary, no network). The estimator alone already covers Claude (the primary model). HF tokenizer backend (`daulet/tokenizers`, cgo) is a follow-up.
- **CCR backends:** in-memory (FIFO + TTL) + SQLite (`modernc.org/sqlite`, pure-Go — no second cgo surface) as production default. Redis is a build-tagged follow-up. Preserve upstream's asymmetry: SQLite is TTL-only (no capacity cap), in-memory is FIFO-capped.
- **cgo-optional core:** tree-sitter code compression, HF tokenizers, and ONNX embeddings are all deferred (follow-ups / kompress-go). The estimator + BM25-fallback relevance + heuristic detector make the whole compression core build without cgo.

---

## 2. The shaping invariant

**Passthrough is sacred; compress only the live zone.** Bytes sent upstream are SHA-256-equal to bytes received from the client, *except* the explicit byte ranges a transform rewrote. We do **byte-range surgery** — copy untouched ranges literally, never deserialize→mutate→reserialize (which would disturb whitespace, key order, and numeric formatting the provider may have cached).

The "live zone" = the latest user message + latest tool results. Everything earlier (system prompt, tool definitions, prior turns, thinking blocks) is the cache-hot frozen prefix and is never mutated.

Six invariants govern the whole pipeline:

| ID | Invariant |
|---|---|
| **I1** | Byte-faithful: output ≡ input except rewritten ranges (SHA-256 round-trip on untouched ranges). |
| **I2** | Hot-zone protection: system, tools, frozen messages never mutated. |
| **I3** | Append-only freeze: once a message appears upstream it freezes; only the live-zone tail is compressible. |
| **I4** | Determinism: same `(input bytes, frozen_count, auth_mode)` → byte-equal output. No timestamps, no random seeds in the path. |
| **I5** | Token-aware reject: compression discarded if output tokens ≥ input tokens (forward original). |
| **I6** | Position-preserving: no reordering/splitting/metadata-injection into original blocks; the CCR marker is appended at the end of a block only. |

---

## 3. Architecture & data flow

```
proxy ┐
mcp   ┼─► body bytes ─► live-zone dispatcher (proxy path; MCP path compresses a single content string)
cli   ┘      1. find frozen floor (cache_control walk)
             2. locate latest user msg, enumerate content blocks
             3. per block (skip cache-hot types):
                  byte-threshold gate
                  ContentRouter.Detect(block) → ContentType
                  route → Pipeline.Run(content, type, ctx, store):
                      reformats (lossless, serial, early-stop at 0.5 ratio)
                      offloads  (info-preserving, gated by cheap bloat estimate)
                  tokenizer reject (I5): keep only if tokens strictly shrink
                  maybe inject CCR marker  <<ccr:HASH>>
             4. plan byte-range replacements → surgical rewrite (untouched ranges copied verbatim)
                                              │
        CCR store (BLAKE3-keyed original) ◄── headroom_retrieve(hash)
```

- **Detect** (`internal/detect`): heuristic classifier → one of 7 `ContentType` variants.
- **Route** (`internal/router`): `ContentType` → registered transforms.
- **Compress** (`internal/pipeline`): orchestrator — lossless reformats first, then gated info-preserving offloads.
- **CCR** (`internal/ccr`): stashes original under a BLAKE3-24-hex key, emits a sentinel marker; `headroom_retrieve` recovers it.
- **Entrypoints** (proxy/MCP/CLI): thin shells over the same core.

---

## 4. Go package layout

```
cmd/
  headroom/              # single multi-command CLI: wrap, proxy, perf, learn, mcp, --version

internal/
  # ── compression core (v0.1) ────────────────────────────────────────
  transform/             # Transform interfaces, Reformat/OffloadOutput, TransformError, CompressionContext
  pipeline/              # orchestrator (reformats→offloads), PipelineConfig (go:embed pipeline.toml)
  detect/                # heuristic content_detector: ContentType + DetectContentType + DetectionResult
  router/                # ContentType → transform dispatch (the ContentRouter)
  ccr/                   # Store iface, ComputeKey (BLAKE3-24), MarkerFor/ParseMarker
  ccr/backends/          # inmemory.go (FIFO+TTL), sqlite.go (modernc)
  tokenizer/             # Tokenizer iface, estimator, tiktoken, registry
  reformats/             # json_minifier.go, log_template.go (lossless)
  offloads/              # json_offload.go, log_offload.go, diff_offload.go, diff_noise.go, search_offload.go
  smartcrusher/          # JSON-array crown jewel; subpkgs: analyzer, classifier, fielddetect, outliers,
                         #   anchors, planning, compaction (compactor/ir/walker/formatter)
  compress/              # log_compressor.go, diff_compressor.go, search_compressor.go (heuristic engines)
  signals/               # ImportanceSignal + KeywordDetector + Tiered combinator
  relevance/             # Score + Scorer iface, bm25.go, hybrid.go (embedding.go follow-up)
  tagprotect/            # protect/restore custom-tag regions
  toolpairs/             # tool_use/tool_result atomicity
  adaptive/              # compute_optimal_k (simplified MVP)
  config/                # HEADROOM_* env+flag schema, precedence
  paths/                 # ~/.headroom layout helpers

  # ── entrypoints / infra (v0.2) ─────────────────────────────────────
  livezone/              # Anthropic live-zone dispatcher: frozen floor, block enum, byte-range surgery, I5
  policy/                # AuthMode classify + CompressionPolicy.ForMode (payg/oauth/subscription tables)
  cachecontrol/          # ComputeFrozenCount walker + TTL-ordering warn
  proxy/                 # Config, AppState, router, forward.go (forward_http), health.go
  headers/               # hop-by-hop drop-lists, XFF injection, x-headroom-* strip
  sse/                   # SseFramer (framing.go) + anthropic.go observer (telemetry)
  cachestab/             # E3 anthropic_cache_control, E4 openai_cache_key, E5 volatile, E6 drift
  memory/                # cross-agent sync (sha256[:16] dedup) + adapters/{claude,codex}
  learn/                 # registry, analyzer, writer, plugins/{claude}
  perf/                  # proxy.log PERF parser + summary

  # ── shared (v0.1) ──────────────────────────────────────────────────
  mcp/                   # stdio MCP server: headroom_compress/_retrieve/_stats
```

One file per concern inside each `internal/<pkg>`, following graphify-go convention.

---

## 5. Core interfaces

### Transform family (`internal/transform`)

```go
type ContentType int
const (
    JsonArray ContentType = iota; SourceCode; SearchResults; BuildOutput; GitDiff; Html; PlainText
)
func (c ContentType) String() string // "json_array","source_code","search","build","diff","html","text"

type CompressionContext struct { Query string; TokenBudget *int }

// Sentinel errors — ALL mean "skip this transform, continue pipeline, never panic".
var (
    ErrInvalidInput = errors.New("invalid input")
    ErrSkipped      = errors.New("skipped")
    ErrInternal     = errors.New("internal")
)

type ReformatOutput struct { Output string; BytesSaved int }              // lossless, no CCR
type OffloadOutput  struct { Output string; BytesSaved int; CacheKey string } // info-preserving, CCR key required

type ReformatTransform interface {
    Name() string
    AppliesTo() []ContentType
    Apply(content string) (ReformatOutput, error)
}
type OffloadTransform interface {
    Name() string
    AppliesTo() []ContentType
    EstimateBloat(content string) float32 // 0..1, cheap structural sniff, NO full pass
    Apply(content string, ctx CompressionContext, store ccr.Store) (OffloadOutput, error)
    Confidence() float32
}
```

### Pipeline orchestrator (`internal/pipeline`)

```go
type Result struct { Output string; BytesSaved int; StepsApplied []string; CacheKeys []string }

type Pipeline struct { /* reformatsByType, offloadsByType, config */ }
func (p *Pipeline) Run(content string, ct transform.ContentType, ctx transform.CompressionContext, store ccr.Store) Result
```

Exact gating (ported faithfully):
- Reformats run **sequentially** in registration order on the running string; skip any with `BytesSaved == 0`; **early-stop** once `len(current)/len(original) <= reformat_target_ratio` (default **0.5**).
- Offloads run after reformats. An offload runs if `score >= bloat_threshold` (default **0.5**) **OR** (`reformatRatio > offload_fallback_ratio` (default **0.85**) **AND** `score > 0`). Sequential, chained on running output; `CacheKeys` populated from accepted offloads only.
- MVP is **sequential** (no rayon-style goroutine fan-out — added only after correctness is proven).

### ContentRouter (`internal/router`)

```go
type Router struct{ /* detector + pipeline */ }
func (r *Router) Detect(content string) detect.DetectionResult
func (r *Router) Compress(content string, ctx transform.CompressionContext, store ccr.Store) pipeline.Result
```

### CCR store (`internal/ccr`)

```go
type Store interface {
    Put(hash, payload string)
    Get(hash string) (string, bool)
    Len() int
}
func ComputeKey(payload []byte) string // BLAKE3 → first 24 lowercase hex (96 bits)
func MarkerFor(hash string) string     // "<<ccr:" + hash + ">>"
func ParseMarker(s string) (hash string, ok bool)

const DefaultCapacity = 1000
const DefaultTTL      = 5 * time.Minute // core default; config layer overrides

type BackendConfig struct { Kind BackendKind; Capacity int; TTLSeconds uint64; Path, URL, KeyPrefix string }
func FromConfig(cfg BackendConfig) (Store, error)
```

> Three marker formats exist and must NOT be unified — each gets its own builder: canonical `<<ccr:HASH>>` (live-zone), comma-form `<<ccr:HASH,KIND,SIZE>>` (compaction opaque cell), space-form `<<ccr:HASH N_rows_offloaded>>` (lossy drop).

### Tokenizer (`internal/tokenizer`)

```go
type Backend int; const ( BackendTiktoken Backend = iota; BackendHuggingFace; BackendEstimation )
type Tokenizer interface { CountText(text string) int; Backend() Backend }

func GetTokenizer(model string) Tokenizer // registered-HF > tiktoken(estimator fallback) > family estimator
// EstimatingCounter: max(1, round(RuneCountInString/cpt)) — round-half-up, RUNE count not bytes.
```

---

## 6. Subsystem port plan

| Subsystem | Go package | Libraries | Simplifications |
|---|---|---|---|
| pipeline/orchestrator | `pipeline`, `transform` | `BurntSushi/toml` (go:embed `pipeline.toml`) | Sequential; token reject lives in `livezone` not orchestrator |
| content detection | `detect` | stdlib `regexp` (RE2), `encoding/json` | Port legacy regex detector fully (all 7 types). Magika ML = follow-up |
| live-zone dispatcher | `livezone` | `tidwall/gjson`+`sjson`, `lukechampine.com/blake3` | Anthropic only for MVP; OpenAI dispatchers follow-up |
| **SmartCrusher (JSON)** | `smartcrusher` (+subpkgs) | `iancoleman/orderedmap`, `crypto/sha256`, `crypto/md5`, `regexp` | **MVP = lossless compaction path** (homogeneous table → `[N]{col:type}` CSV schema + dotted flatten, 0.30 savings gate) + opaque CCR cells + SmartSample lossy fallback + error/outlier/anchor preservation. **Drop byte-parity.** Buckets, stringified-JSON nesting, TopN/TimeSeries/ClusterSample = follow-up |
| log compressor | `compress` (log) + `offloads` | stdlib `strings`+`regexp`, `crypto/md5` | Plain `Contains`/`HasPrefix` (small N); full 6-stage pipeline incl warning-dedup + CCR marker |
| diff compressor | `compress` (diff) + `offloads` | stdlib `regexp`/`strings` | Full parse/score/cap/trim + DiffNoise (lockfile + whitespace-only hunk drop) |
| search compressor | `compress` (search) + `offloads` | stdlib byte-scan, `hash/fnv` | Byte-scan parser w/ Windows-drive guard. SearchOffload implemented but **not registered** (matches upstream) |
| JSON reformat | `reformats` | `encoding/json` (`UseNumber`, `SetEscapeHTML(false)`) | Accept key-reorder divergence w/ never-inflate guard; byte-exact path = follow-up |
| tag protector | `tagprotect` | stdlib byte-scan, `crypto/rand` | Offset-slicing, not `strings.Replace` |
| tool pairs | `toolpairs` | stdlib `encoding/json` | Direct port |
| adaptive sizer | `adaptive` | stdlib | **Simplified**: `clamp(int(len*keepFraction), minK, maxK)`; SimHash+Kneedle+zlib = follow-up |
| CCR reversible | `ccr` | `lukechampine.com/blake3`, `modernc.org/sqlite`, (`redis/go-redis/v9` build-tagged) | InMemory single `sync.Mutex`. BM25 retrieve query = follow-up |
| relevance/RAG | `relevance` | stdlib only for MVP | **MVP = BM25 + Hybrid fallback mode** (zero ML — Rust default ships this way). Embedding tier errors in v0; `hugot` follow-up |
| importance signals | `signals` | stdlib `strings` | swap to Aho-Corasick only if hot; ML tier is a Tiered seam |
| tokenizer | `tokenizer` | `pkoukk/tiktoken-go` + `tiktoken-go-loader` | HF backend (`daulet/tokenizers`, cgo) = follow-up |
| auth/policy | `policy` | stdlib `net/http`, `strings` | Direct port |
| cache-control walk | `cachecontrol` | `tidwall/gjson`, `log/slog` | warn-not-reject on TTL violation |
| cache-stabilization | `cachestab` | `tidwall/gjson`+`sjson`, `hashicorp/golang-lru/v2`, `crypto/sha256` | E3/E4/E5/E6. Self-consistent hashing (no Rust byte-parity). "Move-to-tail" CacheAligner = doc-only, skip |
| proxy server | `proxy`, `headers`, `sse` | `net/http` + `go-chi/chi/v5`, hand-rolled forward | SSE flush-per-chunk is load-bearing. Compresses **requests** (Decision A); responses verbatim |
| CLI/MCP/learn/perf | `cmd/headroom`, `mcp`, `learn`, `perf` | `spf13/cobra`+`pflag`, `mark3labs/mcp-go`, `anthropics/anthropic-sdk-go` | learn: claude plugin first, single Anthropic call. perf: regex+KV parse. wrap: claude+codex |
| config/paths/memory | `config`, `paths`, `memory` | stdlib `flag`/`envconfig`, `dustin/go-humanize`, `modernc.org/sqlite`, `google/uuid` | **MVP = pure-logic cross-agent sync** (sha256[:16] dedup, echo-back prevention, Claude+Codex adapters) + flat SQLite + FTS5. Vectors/semantic = follow-up |

> **SmartCrusher + the heuristic compressors are the value core.** SmartCrusher delivers most token savings deterministically via the lossless compaction table; log/diff/search compressors are zero-dep Go-native and ship complete (only the heavy `compute_optimal_k` machinery is simplified).

---

## 7. Entrypoints

### MCP server (`headroom mcp serve` → `internal/mcp`) — v0.1
- stdio transport; tools surface as `mcp__headroom__<tool>`.
- **`headroom_compress` `{content}`** → compress via `router.Compress`, stash original in local CCR (TTL 3600s), return `{compressed, hash, ...}`.
- **`headroom_retrieve` `{hash, query?}`** → local store first, then proxy fallback `POST {proxyURL}/v1/retrieve`, then not-found. `query` BM25 = follow-up. `HEADROOM_PROXY_URL` (default `http://127.0.0.1:8787`).
- **`headroom_stats` `{}`** → local session JSON. Cross-process aggregation = follow-up.

### Proxy (`headroom proxy` → `internal/proxy`) — v0.2
- Config `HEADROOM_PROXY_*` env + flags (flag > env > default). `HEADROOM_PROXY_UPSTREAM` required; listen default `0.0.0.0:8787`.
- `http.Client` tuned like reqwest: `IdleConnTimeout=90s`, `ForceAttemptHTTP2`, `CheckRedirect→ErrUseLastResponse`, per-request context deadline (600s) + dial timeout (10s). **No client-wide Timeout** (would cut SSE).
- Routes (chi): `GET /healthz`, `GET /healthz/upstream`, `POST /v1/chat/completions`, `POST /v1/responses`, catch-all handling `POST /v1/messages` (Anthropic). All funnel through one `forward_http`.
- `forward_http`: classify auth mode → resolve policy → build upstream URL → drop/copy/rewrite headers (hop-by-hop, host, content-length, `x-headroom-*`, XFF) → **compress request body via live-zone dispatcher (PAYG only; Decision A)** → buffered-vs-streaming branch (413 on oversize) → send → copy status + filtered headers back → inject `x-request-id` + `headroom-upstream-request-id`.
- **Streaming:** detect `Content-Type: text/event-stream`, copy upstream body with explicit `Flusher.Flush()` per chunk, zero buffering. **Responses are never compressed.** SSE framer defers UTF-8 decode until a full event is framed (multibyte-straddle safety).

### CLI (`cmd/headroom` → cobra)
- `--version`/`-v`. (v0.1: `mcp`, `--version`.)
- **`proxy`** (v0.2): assemble config, run server.
- **`wrap <agent>`** (v0.2): probe port → spawn `headroom proxy` if absent → poll 45s → set per-agent env (`ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL` + `~/.codex/config.toml` marker block) → launch subprocess. MVP: claude + codex.
- **`perf`** (v0.2): parse `~/.headroom/logs/proxy.log` PERF lines (preprocess comma-millis timestamp `2006-01-02 15:04:05,fff`), overall + per-model + per-transform summary, `--hours`(168)/`--raw`/`--format`(text|json|csv). Static pricing map.
- **`learn`** (v0.2): claude plugin (decode `~/.claude/projects/<enc>` dirs, scan `*.jsonl`), `classify_error` (15 regexes + 14 substrings verbatim), one Anthropic-SDK analyzer call, marker-bounded idempotent writer to CLAUDE.md/MEMORY.md, dry-run vs `--apply`.

---

## 8. Phasing

### v0.1 — compression engine + MCP (this implementation cycle)
Usable token-saver via the MCP `headroom_compress` tool, fully tested standalone.

1. `transform` interfaces + `TransformError` + `Reformat/OffloadOutput` + `CompressionContext`.
2. `pipeline` orchestrator with exact ordering/gating (reformats early-stop 0.5; offloads 0.5 / fallback 0.85; chain; cache_keys from offloads only). Sequential.
3. `PipelineConfig` via go:embed + toml with exact default `pipeline.toml` values.
4. Heuristic content detector (7 types, ordered dispatch, exact thresholds).
5. **SmartCrusher**: ordered-JSON, classify+crush array (MAX_DEPTH=50), lossless compaction table + CSV schema + dotted flatten + 0.30 gate, opaque CCR cells, error/outlier/anchor preservation, SmartSample lossy fallback, field_detect, analyzer crushability tree.
6. **Heuristic compressors (full)**: LogCompressor (6-stage), DiffCompressor + DiffNoise, SearchCompressor (SearchOffload unregistered), JsonMinifier, LogTemplate; tagprotect; toolpairs; adaptive sizer (simplified).
7. CCR: Store + ComputeKey + marker build/parse + InMemory (FIFO+TTL) + SQLite (modernc) + factory.
8. Tokenizer: estimator (round-half-up, runes) + tiktoken (offline vocab) + registry.
9. Relevance: BM25 + Hybrid fallback. Signals: KeywordDetector + Tiered.
10. MCP server (compress / retrieve-local / stats-local).
11. config/paths schema.
12. **TDD**: SHA-256 byte-equality round-trip for untouched ranges; determinism test (I4); port upstream unit tests (relevance thresholds, tokenizer rounding table, detector boundaries) as Go acceptance tests.
13. Repo scaffold: `go.mod`, Apache-2.0 + NOTICE, README, GOALS.md, CLAUDE.md block, beads, CI (build/vet/test), Uplift release + Homebrew tap, Claude skill.

### v0.2 — proxy + cache-stabilization + memory + CLI extras (next cycle)
1. `policy` (3 exact tables + AuthMode + UA-prefix classify) + `cachecontrol` (ComputeFrozenCount + TTL warn).
2. **Anthropic live-zone dispatcher**: frozen floor, latest-user blocks, cache-hot exclusion, byte-range surgery, token-validation reject (I5), CCR marker `<<ccr:HASH>>`.
3. **Proxy**: forward_http + flushing SSE + framer + health + headers; **request-body compression wired (Decision A)**; responses verbatim.
4. `cachestab` E3–E6.
5. CLI: `proxy`, `wrap` (claude, codex), `perf`, `learn` (claude).
6. Cross-agent memory sync (content-hash dedup, Claude+Codex adapters, flat SQLite + FTS5).

### Follow-ups (tracked in beads, not v0)
Magika ONNX detection; exact Anthropic tokenizer; OpenAI dispatchers; SmartCrusher heavy modes (Buckets/heterogeneous, stringified-JSON nesting, TopN/TimeSeries/ClusterSample, crush_string/number/object with Kneedle); full `compute_optimal_k` (SimHash+Kneedle+zlib); CodeAwareCompressor via tree-sitter (cgo); embedding relevance (hugot/ONNX) + HF tokenizer backend; CCR Redis (build-tagged) + BM25 retrieve query; cachestab E1/E2 + Phase-E auto-inject (PAYG-only); Bedrock SigV4 + Vertex ADC + WebSocket + `/metrics`; memory vectors/semantic-supersede/bridge/graph + gemini adapter + MCP memory tools; CLI copilot/cursor/cline/continue wrap + RTK/lean-ctx + unwrap + codex/gemini learn; rayon-style goroutine fan-out.

---

## 9. Risks & open questions (remaining)

1. **Byte-faithful surgery + determinism (I1, I4)** — the hardest correctness property. Copy untouched ranges literally (no re-serialize); no timestamps/random seeds in the path. **Test with SHA-256 round-trip FIRST, before any compressor logic** (TDD red test #1).
2. **Ordered-JSON up front** — picked (`orderedmap` + `gjson/sjson`); a wrong choice here is expensive to unwind, so it is a foundation task.
3. **CCR trigger threshold not in studied core files** — the numeric per-block store+mark gate lives in proxy/crusher wiring not yet read. Each compressor already carries its own `min_compression_ratio_for_ccr` (log 0.5, diff/search/json 0.8); use those and expose a config knob with a documented default. Do **not** invent a global number.
4. **Streaming hazard (resolved constraint, not a question)** — response-side SSE compression corrupts live token rendering; v0 only compresses request bodies, streams responses verbatim.
5. **kompress-go seam** — `Compressor`/`OffloadTransform` must let the ONNX impl drop in with zero core changes; heuristic detector + BM25-fallback are the self-contained defaults. Honored by §5 interfaces.

---

## 10. Parity checklist

| headroom feature | headroom-go package | Status |
|---|---|---|
| Reformat/Offload traits, TransformError, contexts | `transform` | **v0.1** |
| Pipeline orchestrator (ordering/gating) | `pipeline` | **v0.1** |
| PipelineConfig (toml, exact defaults) | `pipeline` | **v0.1** |
| rayon parallel reformat/bloat | (goroutines) | followup |
| Heuristic content detector (7 types) | `detect` | **v0.1** |
| Magika ONNX detector | `detect` (+kompress-go) | followup |
| Anthropic live-zone dispatcher | `livezone` | **v0.2** |
| OpenAI chat/responses dispatchers | `livezone` | followup |
| Byte-range surgery + I1/I4 | `livezone` | **v0.2** |
| AuthMode classify + CompressionPolicy | `policy` | **v0.2** |
| cache_control frozen-count walk + TTL warn | `cachecontrol` | **v0.2** |
| SmartCrusher: lossless compaction table | `smartcrusher` | **v0.1** |
| SmartCrusher: SmartSample lossy + anchors/outliers | `smartcrusher` | **v0.1** |
| SmartCrusher: Buckets/heterogeneous/nested/TimeSeries/Kneedle | `smartcrusher` | followup |
| SmartCrusher: byte-parity (BUG#1, %g) | — | **skip** (Decision B) |
| LogCompressor (6-stage) + LogOffload + LogTemplate | `compress`,`reformats`,`offloads` | **v0.1** |
| DiffCompressor + DiffOffload + DiffNoise | `compress`,`offloads` | **v0.1** |
| SearchCompressor | `compress` | **v0.1** |
| SearchOffload (registered) | `offloads` | **skip** (impl, unregistered — matches upstream) |
| JsonMinifier reformat | `reformats` | **v0.1** |
| JsonOffload (SmartCrusher-backed) | `offloads` | **v0.1** |
| tag_protector | `tagprotect` | **v0.1** |
| tool-pair atomicity | `toolpairs` | **v0.1** |
| adaptive_sizer compute_optimal_k | `adaptive` | **v0.1** (simplified); full Kneedle followup |
| CodeAwareCompressor (tree-sitter) | `compress` | followup |
| CCR Store + BLAKE3 key + markers | `ccr` | **v0.1** |
| CCR InMemory (FIFO+TTL) / SQLite (prod) | `ccr/backends` | **v0.1** |
| CCR Redis / BM25 retrieve query | `ccr` | followup |
| Tokenizer estimator + tiktoken | `tokenizer` | **v0.1** |
| Tokenizer HF backend | `tokenizer` | followup |
| Relevance BM25 + Hybrid fallback | `relevance` | **v0.1** |
| Relevance Embedding + fusion | `relevance` (+kompress-go) | followup |
| Importance signals (keyword + Tiered) | `signals` | **v0.1** |
| Proxy passthrough + headers + health + flushing SSE | `proxy`,`headers`,`sse` | **v0.2** |
| Proxy request-body compression wired (Decision A) | `proxy` | **v0.2** |
| Bedrock / Vertex / WebSocket / /metrics | `proxy` | followup |
| cachestab E3/E4/E5/E6 | `cachestab` | **v0.2** |
| cachestab E1/E2 + move-to-tail + Phase-E auto-inject | `cachestab` | followup |
| MCP compress / retrieve / stats | `mcp` | **v0.1** |
| MCP cross-process stats / memory tools | `mcp` | followup |
| CLI wrap (claude, codex) / proxy / perf / learn (claude) | `cmd/headroom`,`perf`,`learn` | **v0.2** |
| CLI mcp / --version | `cmd/headroom` | **v0.1** |
| CLI wrap (copilot/cursor/...) + RTK + unwrap + codex/gemini learn | `cmd/headroom`,`learn` | followup |
| config/paths schema | `config`,`paths` | **v0.1** |
| Cross-agent memory sync (hash dedup, claude/codex) + SQLite FTS5 | `memory` | **v0.2** |
| Memory vectors/semantic/graph/MCP tools | `memory` | followup |

---

## 11. Testing strategy

- **TDD throughout** (per superpowers). Red test before impl.
- **Foundation red tests first:** (1) SHA-256 byte-equality round-trip on untouched ranges; (2) determinism (I4) — same input twice → byte-equal output; (3) token-reject (I5) — never inflate.
- **Port upstream unit tests as Go acceptance tests:** relevance thresholds, tokenizer rounding table, detector boundary cases, orchestrator gating (early-stop 0.5, fallback 0.85).
- **Golden fixtures** for SmartCrusher / log / diff / search on representative tool outputs; assert token-reduction ratio bounds, not byte-exactness (Decision B).
- CI runs `go build ./... && go vet ./... && go test ./...`.
