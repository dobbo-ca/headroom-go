# headroom-go

Clean-room Go port of [headroom](https://github.com/chopratejas/headroom) — an
LLM context-compression layer. Compress tool outputs, logs, diffs, and search
results before they reach the model: 60–95% fewer tokens, same answers.

Status: v0.1 in progress (compression engine + MCP server). See
`docs/superpowers/specs/` and `docs/superpowers/plans/`.

## Install

    go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest

## Compressors

The pipeline detects a content type, runs the lossless reformats for that type,
then the information-preserving offloads.

| Transform | Kind | Content type | What it does |
|---|---|---|---|
| `json_minifier` | reformat | `json_array` | Removes insignificant whitespace |
| `log_compressor` | offload | `build` | Five stages, then head-and-tail with the middle in CCR |
| `diff_compressor` | offload | `diff` | Drops lockfile and whitespace-only hunks, caps the rest |

Every offload stashes the original in the CCR store and leaves a
`<<ccr:HASH>>` marker, so anything dropped can be retrieved by hash.

Three rules hold for all of them, and each has a test:

- **Never inflate.** Output is never longer than input.
- **Never panic.** Malformed input is skipped, and the pipeline continues.
- **Deterministic.** The same input gives the same output, every run.

These compressors are clean-room designs, not ports. They do not reproduce
upstream headroom's output, only the same invariants. See
`docs/superpowers/specs/2026-08-19-heuristic-compressors-addendum.md`.

## License

Apache-2.0. See `LICENSE` and `NOTICE`.
