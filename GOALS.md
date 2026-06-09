# GOALS

Reference for future sessions. Tracks the objective, what's done, what's left.

## Objective

Clean-room Go port of headroom (chopratejas/headroom) as dobbo-ca/headroom-go:
an LLM context-compression layer exposed as a drop-in proxy, an MCP server, and
a CLI wrapper, cutting tokens 60–95% while preserving answers.

## Done

- [x] (Plan 1) Foundation: transform interfaces, CCR, tokenizer, detector, pipeline, router.

## Follow-ups

See the spec's §8 phasing and §10 parity checklist.

## Out of scope (deferred to follow-ups / kompress-go)

ML prose compression (ONNX Kompress), Python framework integrations, byte-parity
with upstream, Bedrock/Vertex/WebSocket transports.
