# headroom-go

Clean-room Go port of [headroom](https://github.com/chopratejas/headroom) — an
LLM context-compression layer. Compress tool outputs, logs, diffs, and search
results before they reach the model: 60–95% fewer tokens, same answers.

## Quickstart

```bash
brew install dobbo-ca/taps/headroom-go
headroom wrap claude
```

That is the whole thing. `headroom wrap` starts the proxy, points Claude Code
at it, gives Claude a headroom MCP server on the same CCR store so it can
dereference the markers it sees, and stops both when Claude exits.

After a day of use, ask whether it was worth it:

```bash
headroom perf
```

Status: v0.1 (compression engine, proxy, MCP server, wrap, perf). See
`docs/superpowers/specs/` and `docs/superpowers/plans/`.

## Install

```bash
brew install dobbo-ca/taps/headroom-go
```

Or download a binary from [Releases](https://github.com/dobbo-ca/headroom-go/releases)
— darwin, linux, and windows, amd64 and arm64. Or build from source:

```bash
go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest
```

## Semantic cache (opt-in, off by default)

The gateway can return a stored provider response when a new request is
semantically close to one already seen. Closeness is measured by embedding both
requests with a local model.

The model runs in its own process and is reached over HTTP, so the binary stays
`CGO_ENABLED=0` and cross-compiles:

```bash
ollama serve
ollama pull nomic-embed-text
```

| Option | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Off unless explicitly turned on. |
| `endpoint` | `http://localhost:11434` | Where the embedding model listens. |
| `model` | `nomic-embed-text` | Part of the cache key; changing it invalidates stored entries. |
| `threshold` | `0.97` | Minimum cosine score for a hit. |
| `max_entries` | `2000` | Vector index bound. |
| `timeout` | `2s` | Embedding call budget; expiry means miss. |

**A cache hit returns a stored answer to a different question.** Every other
transform in headroom-go is reversible or information-preserving; this one is
not. Two requests about two different files can score above any threshold you
pick. Three rules follow, and the code enforces all three:

- A request carrying tool results is never cached and never served.
- A missing or failing model is a miss, never an error.
- The cache is off until you turn it on.

If Ollama is not running, every lookup misses and the gateway behaves exactly
as it does with the cache disabled.

## Use it as a proxy

`headroom proxy` sits in front of an upstream LLM API. It compresses REQUEST
bodies through the live-zone dispatcher before forwarding them; responses
always stream back verbatim — a response is never compressed or rewritten,
because rewriting one corrupts live token rendering.

```bash
export HEADROOM_PROXY_UPSTREAM=https://api.anthropic.com
headroom proxy
```

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--upstream` | `HEADROOM_PROXY_UPSTREAM` | — (required) | Upstream API base URL |
| `--listen` | `HEADROOM_PROXY_LISTEN` | `0.0.0.0:8787` | Address the proxy listens on |
| — | `HEADROOM_PROXY_MAX_BODY_BYTES` | `33554432` (32 MiB) | Request body cap; `0` means uncapped |
| — | `HEADROOM_PROXY_TIMEOUT_SECONDS` | `600` | Per-request context deadline (there is no client-wide timeout, so long SSE streams are never cut) |
| — | `HEADROOM_PROXY_COMPRESS` | on | Set to `disabled`, `off`, `false`, `0`, or `no` to forward request bodies unmodified |
| — | `HEADROOM_PROXY_REPLAY` | on | Set to `disabled`, `off`, `false`, `0`, or `no` to stop re-sending a compressed block in its compressed form on every later turn of the same session |

#### `HEADROOM_PROXY_REPLAY`

Without replay, headroom saves nothing on an agent client such as Claude Code.
The client marks its newest message with `cache_control` and re-sends the whole
conversation every turn, so the frozen floor swallows the entire body.

That marker is a cache **write** instruction for bytes the provider has never
seen, not a read guarantee. Replay lets headroom compress the newest message
and then reproduce those exact bytes on every later turn, so the provider's
cached prefix keeps matching. Compressing without replaying is worse than doing
nothing: the client re-sends the original, the prefix no longer matches, and
every turn pays a fresh cache write.

Replay is on by default. Three guard rails hold it up:

- **A client that declares no session gets no replay.** The identity must come
  from `x-headroom-session-id` or `x-claude-code-session-id`. The fallbacks the
  drift detector uses — a credential, or the client address and User-Agent —
  identify a tenant rather than a conversation, and the address one rotates per
  TCP connection. A client sending neither header still works; replay is a
  no-op and logs one warning.
- **A marker whose original the store cannot hand back never goes on the
  wire.** Every accepted block has its markers read back first, in both
  surfaces: the canonical `<<ccr:HASH>>` and the compressors' inline `hash=`.
  A block that fails is forwarded untouched.
- **Entries are swept once the client stops re-sending their block.** A block
  still in the conversation is looked up every turn, so an entry unseen for
  three turns has scrolled out. A long-running proxy holds the live working set
  rather than the whole day.

**Run the MCP server against the same CCR store.** With replay on, the model no
longer sees its own earlier tool results and recovers them by calling
`headroom_retrieve`. `headroom wrap` wires that for you and refuses to start a
session it cannot wire; if you run `headroom proxy` yourself, start
`headroom mcp serve --ccr-path` on the same file.

`GET /healthz` reports the proxy itself; `GET /healthz/upstream` checks the
configured upstream. `POST /v1/retrieve` is headroom's own route — it is
served locally and never forwarded upstream — and resolves a `<<ccr:HASH>>`
marker back to the original bytes it replaced.

### `headroom wrap`

One command brings up everything and tears it down again:

```bash
headroom wrap claude
headroom wrap claude -p 'summarise this repo' --model sonnet
headroom wrap codex --upstream https://api.openai.com
```

It reuses a proxy already listening at `HEADROOM_PROXY_URL`, and otherwise runs
one **in its own process** — so the proxy cannot outlive the agent and cannot
die behind its back. The upstream defaults per agent, so neither
`HEADROOM_PROXY_UPSTREAM` nor a `--` separator is needed.

For `claude` it also passes an inline `--mcp-config` that launches
`headroom mcp serve` against the same CCR store the proxy writes to, which is
what lets the model dereference a `<<ccr:HASH>>`.

If it cannot wire that server — the agent takes no inline MCP flag, or the
store is in memory and so unshareable — then with replay **on** it refuses to
start, and with replay **off** it warns and runs. An unresolvable marker costs
one turn without replay and the whole session with it.

## Was it worth it?

```bash
headroom perf
```

`headroom perf` joins headroom's own ledger — one line per turn at
`~/.headroom/ledger.jsonl` — to the usage records Claude Code already writes
under `~/.claude/projects`. It reports what headroom removed *and* what the
prompt cache did about it, because bytes saved with a busted cache is a loss.

```
WHAT HEADROOM DID
  turns             142   (96 compressed)
  bytes sent        18.9 MB of 20.6 MB
  bytes saved       8.3%   (wire bytes; not tokens, not the bill)
  blocks replayed   311
  strategies        log_offload 61, log_template 35

WHAT THE CACHE DID          from Claude Code's own usage records
  cache read        4,102,881 tok   94.1%   billed 0.1x
  cache write 1h      210,004 tok    4.8%   billed 2.00x
  fresh input          47,551 tok    1.1%   billed 1.0x
  cache-read share  94.1%
  same, unproxied   96.3%   over 2,419 sessions that did not go through headroom
  prefix rewrites   14 turns   early_messages 14
```

Pricing uses Anthropic's real multipliers: a cached read is billed at 0.1x, a
five-minute cache write at 1.25x, and a one-hour write at 2.0x. Claude Code
uses the one-hour TTL, so merging the two writes would understate the cache's
cost badly.

| Flag | Default | Meaning |
|---|---|---|
| `--ledger` | `~/.headroom/ledger.jsonl` | Ledger file to read |
| `--transcripts` | `~/.claude/projects` | Claude Code project directory |
| `--since` | all | Only count turns newer than this, e.g. `24h` |
| `--json` | off | Emit the report as JSON |

The unproxied figure is a different set of sessions, not a control; the report
labels it as such. Every number that combines the two sources is withheld when
they cannot be describing the same traffic.

## MCP server

```bash
headroom mcp serve
```

Register it with an MCP client, for example Claude Code:

```bash
claude mcp add headroom -- headroom mcp serve
```

Three tools are exposed over stdio:

| Tool | Arguments | Returns |
|---|---|---|
| `headroom_compress` | `content` (required), `query` | `compressed`, `hash`, `content_type`, `original_tokens`, `compressed_tokens`, `bytes_saved`, `steps_applied`, `cache_keys` |
| `headroom_retrieve` | `hash` (required), `query` (reserved) | `found`, `source` (`local`, `proxy`, or `none`), `hash`, `content` |
| `headroom_stats` | none | Session counters: calls, bytes, tokens, and CCR hit rates |

`headroom_compress` never returns text that costs more tokens than its input
(I5). When compression would not help, it returns the original verbatim with
`bytes_saved: 0` and an empty `hash`.

### Configuration

Precedence is flag, then environment variable, then default.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `--ccr-backend` | `HEADROOM_CCR_BACKEND` | `sqlite` | `sqlite` or `memory` |
| `--ccr-path` | `HEADROOM_CCR_PATH` | `~/.headroom/ccr.db` | SQLite store file |
| `--proxy-url` | `HEADROOM_PROXY_URL` | `http://127.0.0.1:8787` | Retrieve fallback base URL |
| `--model` | `HEADROOM_MODEL` | `claude` | Model name used for token counting |
| — | `HEADROOM_CCR_TTL_SECONDS` | `3600` | CCR entry lifetime |
| — | `HEADROOM_CCR_CAPACITY` | `1000` | In-memory FIFO cap; SQLite ignores it |
| — | `HEADROOM_HOME` | `~/.headroom` | Base directory for all of the above |

## Claude skill

`skills/headroom/SKILL.md` teaches an agent when to route large tool output
through `headroom_compress` instead of reading it raw. Copy it into your
project's `.claude/skills/` or point your plugin at it.

## License

Apache-2.0. See `LICENSE` and `NOTICE`.
