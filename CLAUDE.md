# CLAUDE.md

Project guidelines. Merge with global guidelines.

## Starting a session here

**Given no instruction — "continue", `/dobbo:start`, or nothing — run `bd ready`
and work the P0 bead titled `SESSION BRIEF`.** Its description is the whole
brief: where things stand, the goal as a question, the traps that will bite, and
what a finished answer looks like. Read it before asking what to work on.
There is at most one, and every other bead is P1 or lower, so a P0 here means
"this is the session".

Two things that mislead a fresh session, both measured:

- **This checkout is usually stale.** Work happens in worktrees, so the primary
  checkout's `main` sits months behind by design, and the session-start git
  snapshot repeats its SHA — confirming the wrong answer rather than flagging it.
  Run `git fetch origin && git log --oneline origin/main -3` and read from a
  worktree cut from `origin/main`.
- **`bd prime`'s output is larger than the host will show.** It arrives
  truncated to a preview, so memories past the first page are absent from
  context. Read the persisted hook output when it says it was truncated; that is
  where the hard-won detail lives.

When the briefed work is done, close that bead and leave the next `SESSION BRIEF`
behind for whatever the work actually revealed.

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
