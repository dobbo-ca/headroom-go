# GOALS

Reference for future sessions. Tracks the objective, what's done, what's left.

## Objective

Clean-room Go port of headroom (chopratejas/headroom) as dobbo-ca/headroom-go:
an LLM context-compression layer exposed as a drop-in proxy, an MCP server, and
a CLI wrapper, cutting tokens 60–95% while preserving answers.

## Done

- [x] (Plan 1) Foundation: transform interfaces, CCR, tokenizer, detector, pipeline, router.
- [x] (Plan 2) Heuristic compressors: reformats (json_minifier, log_template),
      compress engines (log 6-stage, diff, search), offloads
      (log/diff/diff_noise/json; search built-unregistered), signals,
      relevance (BM25+Hybrid), adaptive (simplified), tagprotect, toolpairs;
      wired into router.NewDefault. SmartCrusher (JSON crush) is Plan 3.
- [x] (Plan 3) SmartCrusher: lossless compaction table + opaque CCR cells + SmartSample lossy fallback + field_detect + analyzer crushability tree (MAX_DEPTH=50); swapped into JsonOffload. Deferred: Buckets/heterogeneous, stringified-JSON deep nesting, TopN/TimeSeries/ClusterSample, crush_string/number/object + full compute_optimal_k (Kneedle/SimHash/zlib), round-trip recovery of lossless-table opaque cells.
- [x] (Plan 4) Entrypoints: internal/paths (~/.headroom layout), internal/config
      (HEADROOM_* schema, flag>env>default), internal/mcp (stdio server with
      headroom_compress / headroom_retrieve / headroom_stats), and the
      cmd/headroom cobra CLI. v0.1 spec items 10 and 11 are complete.
- [x] (Plan 5) v0.2 invariant core: internal/policy (AuthMode classify + 3-row
      CompressionPolicy table), internal/cachecontrol (ComputeFrozenCount +
      TTL-order warn), internal/livezone (Anthropic live-zone dispatcher:
      frozen floor, block planning, byte-range surgery via gjson offsets,
      I5 token reject, CCR markers). v0.2 spec items 1 and 2 are complete.
      Deferred: the proxy server itself (item 3), POST /v1/retrieve, sjson
      (lands with cachestab E3/E4), OpenAI dispatchers.
- [x] (Plan 6) v0.2 proxy: internal/proxy (config, server, forward with
      live-zone request compression, health, local POST /v1/retrieve),
      cmd/headroom proxy and wrap (claude, codex). v0.2 spec items 3 and 5
      are complete; headroom is usable as a drop-in proxy. Zero new
      dependencies: stdlib ServeMux replaces chi, httputil.ReverseProxy is
      the hop (hop-by-hop and Connection-listed header stripping, XFF,
      flush-per-chunk SSE), and no SSE framer is needed because responses
      are never parsed. Deferred: cachestab E3-E6, perf, learn, memory sync,
      OpenAI live-zone dispatchers.

- [x] (Plan 6) Repo scaffold (v0.1 item 13): tag-push release workflow building
      six reproducible `CGO_ENABLED=0` binaries, and `skills/headroom/SKILL.md`.
      Deferred: `Formula/headroom-go.rb` in dobbo-ca/homebrew-taps, which needs
      the digests of a real release before it can be seeded. Item 13's "Uplift"
      was dropped in favour of tag-push; there is no `.uplift.yml`, `.cliff.toml`,
      or `CHANGELOG.md`.

## Follow-ups

See the spec's §8 phasing and §10 parity checklist.

## Out of scope (deferred to follow-ups / kompress-go)

ML prose compression (ONNX Kompress), Python framework integrations, byte-parity
with upstream, Bedrock/Vertex/WebSocket transports.
