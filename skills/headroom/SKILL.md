---
name: headroom
description: Use when a command or tool has returned a large blob of output — a log file, a diff, a JSON payload, a search result, a test run — and you are about to read or quote it. Compress it through the headroom MCP server first, then read the compressed form. Triggers on large tool output, "this log is huge", "trim this diff", "too many tokens", "context is filling up", and on any output over a few hundred lines.
---

# headroom — compress tool output before you read it

Large tool output is the main way a context window fills up. headroom shrinks
logs, diffs, JSON, and search results by 60–95% while keeping the parts that
answer a question. The original is never lost: it is stored under a hash you
can retrieve.

## The loop

1. A tool returns a big blob.
2. Call `headroom_compress` with `content` set to that blob, and `query` set to
   what you are trying to find out. The query steers which lines survive.
3. Read the `compressed` field. Work from that.
4. If the compressed form dropped something you now need, call
   `headroom_retrieve` with the `hash` to get the original back.

## When to reach for it

- A log or test run over a few hundred lines.
- A diff you only need the shape of.
- A JSON API response with a long, repetitive array.
- A search or grep result with many hits.

## When not to

- Output under about 50 lines. The round trip costs more than it saves.
- Source code you are about to edit. Read the file directly; you need exact
  bytes and exact line numbers.
- Anything you must quote verbatim — an error string, a checksum, a command.
  Compression is not byte-preserving for the parts it rewrites.

## Reading the result

`headroom_compress` returns `original_tokens` and `compressed_tokens` alongside
`compressed`. It never returns something more expensive than what you gave it:
when compression would not help, you get the original back unchanged, with
`bytes_saved: 0` and an empty `hash`. An empty `hash` therefore means there is
nothing to retrieve, because nothing was replaced.

`steps_applied` names which transforms ran, and `content_type` names what
headroom decided the blob was. Both are useful when the result surprises you.

`headroom_stats` reports session totals — calls, bytes, tokens, and cache hit
rates. Use it to answer "how much did this actually save?", not as part of the
compress loop.

## If the tools are missing

The server is a local binary. Install and register it:

```bash
brew install dobbo-ca/taps/headroom-go
claude mcp add headroom -- headroom mcp serve
```

Compressed content is stored in a local SQLite file at `~/.headroom/ccr.db`.
Nothing leaves the machine.
