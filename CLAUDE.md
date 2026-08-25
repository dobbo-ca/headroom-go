# CLAUDE.md

Project guidelines. Merge with global guidelines.

## What this is

Clean-room Go port of headroom (chopratejas/headroom). See
`docs/superpowers/specs/2026-06-08-headroom-go-core-design.md` for the
architecture, invariants (I1–I6), and the parity checklist.

## Conventions

- `cmd/headroom` is the single multi-command CLI. `internal/<pkg>` holds one
  concern per package, one file per concern.
- The core is cgo-optional: no tree-sitter / ONNX / HF tokenizers in v0.
- Compression is deterministic (I4): no timestamps or random seeds on the
  compression path. The CCR store is the only place originals live.
- Drop byte-parity with upstream; keep CCR markers self-consistent.

## Live API tests

`internal/proxy/live_test.go` talks to the real `api.anthropic.com`. It is
behind the `liveapi` build tag, not `t.Skip`, so it cannot report a silent
pass. CI never sets the tag.

```bash
# needs no credential — real TLS, real headers, real 401 envelope
go test -tags liveapi ./internal/proxy/ -run TestLiveUnauthenticated -v

# the rest need a PAYG key; the test rejects an sk-ant-oat subscription token
ANTHROPIC_API_KEY=sk-ant-api... go test -tags liveapi ./internal/proxy/ -run TestLive -v

# rewrite the captured fixtures
HEADROOM_CAPTURE=1 go test -tags liveapi ./internal/proxy/ -run TestLive
```

`internal/proxy/testdata/live/` holds those captured bytes.
`livefixture_test.go` replays them in the ordinary suite — no tag, no network.

`claude_code_request_redacted.json` is a real `claude -p` request body with
every string key and value replaced by same-length filler, so structure and
byte sizes are real and nothing identifying survives. Regenerate it only with
a scrubber that replaces **keys as well as values**: JSON Schema property
names inside `tools` disclose which MCP servers are installed.
