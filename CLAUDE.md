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
