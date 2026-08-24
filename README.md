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

Releases are cut by pushing a `v*` tag. Every binary is built with
`CGO_ENABLED=0` and stamped only with its version, so a rebuild from the same
tag produces the same bytes.

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
