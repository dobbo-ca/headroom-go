# headroom-go

Clean-room Go port of [headroom](https://github.com/chopratejas/headroom) — an
LLM context-compression layer. Compress tool outputs, logs, diffs, and search
results before they reach the model: 60–95% fewer tokens, same answers.

Status: v0.1 in progress (compression engine + MCP server). See
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
| — | `HEADROOM_PROXY_REPLAY` | off | Set to `enabled`, `on`, `true`, `1`, or `yes` to re-send a compressed block in its compressed form on every later turn of the same session |

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

Two things to know before turning it on:

- **Run `headroom mcp serve` against the same CCR store.** With replay on, the
  model no longer sees its own earlier tool results and recovers them by
  calling `headroom_retrieve`. Without the MCP server it sees `<<ccr:HASH>>`
  markers it cannot dereference, for the whole session rather than one turn.
- **Sessions are identified by `x-claude-code-session-id`**, falling back to
  `x-headroom-session-id`, then to a credential-and-first-message fingerprint.
  A client that sends neither header still works; replay simply does not fire,
  and the proxy behaves exactly as it does with replay off.

`GET /healthz` reports the proxy itself; `GET /healthz/upstream` checks the
configured upstream. `POST /v1/retrieve` is headroom's own route — it is
served locally and never forwarded upstream — and resolves a `<<ccr:HASH>>`
marker back to the original bytes it replaced.

### `headroom wrap`

`headroom wrap claude` (or `headroom wrap codex`) starts the proxy if one
is not already running, points the agent's base URL at it, and execs the
agent CLI:

```bash
headroom wrap claude
headroom wrap codex --upstream https://api.openai.com
```

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
