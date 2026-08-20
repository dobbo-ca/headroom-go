# Heuristic compressors — spec addendum

**Amends:** `2026-06-08-headroom-go-core-design.md`, section 8 v0.1 item 6
**Bead:** `hr-47g.26`

## 1. Why this addendum exists

The core design names the heuristic compressors but never defines them. Line
238 says "full 6-stage pipeline incl warning-dedup" and does not list the six
stages. Line 239 says "Full parse/score/cap/trim" and does not define the score
or the cap.

`CLAUDE.md` makes this a clean-room port, so the missing internals were
designed fresh rather than recovered by reading upstream. This document is
where those decisions live, so a reader can tell designed-here behaviour from
ported behaviour.

**Divergence warning.** The core design already dropped byte-parity with
upstream (Decision B). This goes further: the compressors here will not produce
upstream's *output* on the same input, only output that satisfies the same
invariants. Do not use upstream fixtures as expected values.

## 2. Scope

| Compressor | Kind | In this addendum |
|---|---|---|
| JsonMinifier | Reformat | yes |
| LogCompressor | Offload | yes |
| DiffCompressor | Offload | yes |
| SearchCompressor | Offload | deferred |
| LogTemplate | Reformat | deferred |
| tagprotect | — | deferred |
| toolpairs | — | deferred |
| adaptive sizer | — | deferred |

The deferred items stay in the core design's v0.1 list. They are simply not in
this cycle.

## 3. Invariants every compressor here obeys

These are requirements, not preferences. Each gets a test.

- **Never inflate.** If the output is not shorter than the input, return the
  input and report `BytesSaved == 0`. The pipeline already skips a transform
  that saves nothing.
- **Never panic.** Malformed input returns a sentinel error from
  `transform` — `ErrInvalidInput`, `ErrSkipped`, or `ErrInternal`. The pipeline
  skips and continues.
- **Deterministic (I4).** No timestamps, no random seeds, no map iteration
  order in output. The same input gives the same output.
- **Every offload is recoverable.** An `OffloadOutput` carries a `CacheKey`
  that resolves in the store it was given. Anything dropped is in the original
  behind that key.

## 4. JsonMinifier (reformat, lossless)

Removes insignificant whitespace from JSON. Applies to `JsonArray`.

Decode with `UseNumber` so numeric literals keep their exact text, and encode
with `SetEscapeHTML(false)` so `<`, `>`, and `&` are not expanded.

**Accepted divergence:** Go's `encoding/json` reorders object keys. The core
design already accepts this (line 241). The never-inflate guard is what makes
it safe — a reordering that grows the payload is discarded.

Input that is not valid JSON returns `ErrInvalidInput`.

## 5. LogCompressor (offload)

Applies to `BuildOutput`. Five stages, each a pure function from string to
string, run in order on the running text.

The original goes to the CCR store before any stage runs, and the output
carries its key.

| # | Stage | Lossy | Recoverable |
|---|---|---|---|
| 1 | Strip ANSI escape sequences | yes | via CCR |
| 2 | Collapse runs of identical lines to one line plus a count | yes | via CCR |
| 3 | Deduplicate warnings by message body, first kept, with a count | yes | via CCR |
| 4 | Drop progress and spinner lines | yes | via CCR |
| 5 | Keep head and tail, offload the middle behind a marker | yes | via CCR |

### Dropped from the original six

The core design's "6-stage" included trimming a shared timestamp prefix when
every line carries the same date. **That stage is not implemented.** It
discards bytes with no recovery path that the CCR marker covers, unlike the
five above, which all leave the full original retrievable under one key. Five
stages, not six, is a deliberate divergence from line 238.

### Stage rules

1. **ANSI.** Remove CSI sequences: `ESC [` followed by parameter and
   intermediate bytes, ending in a byte in `@`–`~`.
2. **Identical runs.** Two or more consecutive byte-identical lines become the
   first line followed by a repeat count on its own line. A run of one is
   untouched.
3. **Warning dedup.** A line is a warning when it contains `warning:` or
   `WARN`, case-insensitively. Group by the text after that marker. Keep the
   first occurrence; replace the rest with a single count line. Order of first
   occurrences is preserved, so the result is deterministic.
4. **Progress lines.** Drop any line containing a carriage return that is not
   the line terminator — that is output a terminal would have overwritten.
5. **Middle offload.** Above a line-count threshold, keep the first N and last
   N lines and replace the middle with `ccr.MarkerFor(key)`. Below the
   threshold, this stage is a no-op.

`EstimateBloat` is a cheap structural sniff, per the interface contract: line
count and the share of lines matching the progress and warning shapes. It must
not run the stages.

## 6. DiffCompressor (offload)

Applies to `GitDiff`. Parses unified diff into hunks, scores each hunk, drops
the ones that score as noise, and caps how many survive.

### Parse

A hunk starts at a line beginning `@@`. Everything before the first `@@` is the
file header and is always kept. A hunk belongs to the file named by the most
recent `diff --git`, `+++`, or `---` line above it.

### Noise score

A hunk is noise when either holds:

- **Lockfile.** Its file path base name is a known lockfile: `go.sum`,
  `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`,
  `poetry.lock`, `Gemfile.lock`, `composer.lock`.
- **Whitespace-only.** Every added and removed line in the hunk is equal to its
  counterpart after removing all whitespace.

### Cap

After noise removal, keep at most a configured number of hunks, in file order.
Dropped hunks are replaced by one marker line each.

The original diff goes to CCR before parsing; the output carries its key.

`EstimateBloat` counts `@@` occurrences and lockfile path matches without
parsing hunk bodies.

## 7. Configuration

**Not in `pipeline.toml`.** `pipeline.Config` holds the three orchestrator
gating thresholds and nothing else. Adding compressor knobs there would make
the orchestrator know about every compressor that exists, and every new
compressor would widen a struct the pipeline tests already depend on.

Each compressor owns its own config struct with a `DefaultXConfig()`
constructor, and the caller that registers it passes one. The app-level config
package (v0.1 item 11, a later plan) is where these get read from disk.

| Compressor | Field | Default | Used by |
|---|---|---|---|
| LogCompressor | `HeadLines` | `50` | stage 5 |
| LogCompressor | `TailLines` | `50` | stage 5 |
| LogCompressor | `MinLinesToOffload` | `200` | stage 5 threshold |
| DiffCompressor | `MaxHunks` | `40` | cap |

## 8. Testing

Beyond one test per stage and rule:

- **Never-inflate**, per compressor, on input that would grow.
- **Determinism**, per compressor: the same input twice gives identical output.
- **Round-trip**, per offload: the emitted `CacheKey` resolves in the store and
  returns the exact original.
- **Malformed input**: returns a sentinel error, never a panic.
- **Pipeline integration**: with these registered, `router.Compress` is no
  longer a passthrough for `json_array`, `build`, and `diff`.

## 9. Out of scope

The deferred compressors in section 2. Upstream output parity of any kind.
Byte-exact JSON key ordering. The `compute_optimal_k` machinery, which the core
design already lists as a follow-up.
