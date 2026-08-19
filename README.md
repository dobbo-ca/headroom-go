# headroom-go

Clean-room Go port of [headroom](https://github.com/chopratejas/headroom) — an
LLM context-compression layer. Compress tool outputs, logs, diffs, and search
results before they reach the model: 60–95% fewer tokens, same answers.

Status: v0.1 in progress (compression engine + MCP server). See
`docs/superpowers/specs/` and `docs/superpowers/plans/`.

## Install

    go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest

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

## License

Apache-2.0. See `LICENSE` and `NOTICE`.
