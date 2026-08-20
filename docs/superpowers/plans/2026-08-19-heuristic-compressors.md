# Heuristic Compressors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register the first three real transforms so the compression pipeline stops running as a faithful passthrough.

**Architecture:** Two new packages. `internal/reformats` holds lossless transforms — `JsonMinifier`. `internal/compress` holds information-preserving offloads — `LogCompressor` and `DiffCompressor` — each built from pure stage functions and each stashing the original in the CCR store before it drops anything. Both packages implement the interfaces in `internal/transform`, so the pipeline needs no change to accept them.

**Tech Stack:** Go 1.25, stdlib only (`encoding/json`, `regexp`, `strings`, `fmt`, `sort`), plus the existing `internal/transform` and `internal/ccr`.

**Spec:** `docs/superpowers/specs/2026-08-19-heuristic-compressors-addendum.md`, which amends `docs/superpowers/specs/2026-06-08-headroom-go-core-design.md`.

## Global Constraints

- **`CGO_ENABLED=0` must hold.** No new cgo dependency, direct or transitive.
- **No new entries in `go.mod` or `go.sum`.** Stdlib plus existing internal packages.
- **Never inflate.** If output is not shorter than input, return the input and report `BytesSaved == 0`.
- **Never panic.** Malformed input returns a `transform` sentinel — `ErrInvalidInput`, `ErrSkipped`, or `ErrInternal` — wrapped with `%w`.
- **Deterministic (I4).** No timestamps, no random seeds, no map-iteration order in output. Same input, same output, every run.
- **Every offload is recoverable.** `OffloadOutput.CacheKey` must resolve in the store it was given and return the exact original.
- **`EstimateBloat` must not run the stages.** The interface says "cheap structural sniff, NO full pass" (`internal/transform/transform.go`).
- **Package layout:** `internal/<pkg>`, one concern per package, one file per concern (project `CLAUDE.md`).
- **Do not use upstream fixtures as expected values.** This is a clean-room port and output parity with upstream is explicitly not a goal (addendum section 1).

---

### Task 1: `internal/reformats` — JsonMinifier

The simplest transform, and the one that proves the `ReformatTransform` seam works end to end.

**Files:**
- Create: `internal/reformats/json.go`
- Test: `internal/reformats/json_test.go`

**Interfaces:**
- Consumes: `transform.ReformatTransform`, `transform.ReformatOutput`, `transform.ContentType`, `transform.ErrInvalidInput`, `transform.ErrInternal` — all already exist.
- Produces:
  - `type JsonMinifier struct{}`
  - `func (JsonMinifier) Name() string` → `"json_minifier"`
  - `func (JsonMinifier) AppliesTo() []transform.ContentType`
  - `func (JsonMinifier) Apply(content string) (transform.ReformatOutput, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/reformats/json_test.go`:

```go
package reformats

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestJsonMinifierName(t *testing.T) {
	if got := (JsonMinifier{}).Name(); got != "json_minifier" {
		t.Errorf("Name = %q, want json_minifier", got)
	}
}

func TestJsonMinifierAppliesToJsonArray(t *testing.T) {
	types := (JsonMinifier{}).AppliesTo()
	if len(types) != 1 || types[0] != transform.JsonArray {
		t.Errorf("AppliesTo = %v, want [JsonArray]", types)
	}
}

func TestJsonMinifierStripsWhitespace(t *testing.T) {
	in := "[\n  1,\n  2,\n  3\n]"
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != "[1,2,3]" {
		t.Errorf("Output = %q, want [1,2,3]", out.Output)
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Errorf("BytesSaved = %d, want %d", out.BytesSaved, len(in)-len(out.Output))
	}
}

func TestJsonMinifierPreservesNumericLiterals(t *testing.T) {
	// UseNumber keeps the exact text. Without it, 1.0 becomes 1 and a large
	// integer loses precision through float64.
	in := `[1.0, 1e3, 12345678901234567890]`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for _, want := range []string{"1.0", "1e3", "12345678901234567890"} {
		if !strings.Contains(out.Output, want) {
			t.Errorf("Output %q lost literal %q", out.Output, want)
		}
	}
}

func TestJsonMinifierDoesNotEscapeHTML(t *testing.T) {
	in := `{"k": "a<b>c&d"}`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if strings.Contains(out.Output, `<`) {
		t.Errorf("Output %q escaped HTML; SetEscapeHTML(false) not applied", out.Output)
	}
}

func TestJsonMinifierNeverInflates(t *testing.T) {
	// Already minimal: re-encoding cannot be shorter, so the input comes back
	// with BytesSaved 0.
	in := `[1,2,3]`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input %q back", out.Output, in)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
}

func TestJsonMinifierRejectsInvalidJSON(t *testing.T) {
	_, err := (JsonMinifier{}).Apply("not json at all")
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestJsonMinifierIsDeterministic(t *testing.T) {
	// Map key order must not vary between runs. encoding/json sorts map keys,
	// which is what makes this hold.
	in := `{"z":1,"a":2,"m":3,"b":4,"y":5}`
	first, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 20; i++ {
		next, err := (JsonMinifier{}).Apply(in)
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output {
			t.Fatalf("run %d gave %q, first gave %q", i, next.Output, first.Output)
		}
	}
}

func TestJsonMinifierEmptyInputIsInvalid(t *testing.T) {
	if _, err := (JsonMinifier{}).Apply(""); !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/reformats/`
Expected: FAIL — `undefined: JsonMinifier`.

- [ ] **Step 3: Write the implementation**

Create `internal/reformats/json.go`:

```go
// Package reformats holds lossless transforms: the output is semantically
// equivalent to the input and there is no CCR backing.
package reformats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// JsonMinifier removes insignificant whitespace from JSON.
//
// Go's encoding/json reorders object keys, a divergence the core design
// accepts. The never-inflate guard is what makes it safe: a reordering that
// grows the payload is discarded and the input is returned unchanged.
type JsonMinifier struct{}

func (JsonMinifier) Name() string { return "json_minifier" }

func (JsonMinifier) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.JsonArray}
}

// Apply minifies content. UseNumber keeps numeric literals as their exact
// source text; without it 1.0 becomes 1 and large integers lose precision
// through float64. SetEscapeHTML(false) stops <, > and & expanding to \u00xx,
// which would inflate the output.
func (JsonMinifier) Apply(content string) (transform.ReformatOutput, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("json_minifier: decode: %w", transform.ErrInvalidInput)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("json_minifier: encode: %w", transform.ErrInternal)
	}

	// Encoder.Encode appends a newline that the input did not have.
	out := strings.TrimSuffix(buf.String(), "\n")

	if len(out) >= len(content) {
		return transform.ReformatOutput{Output: content, BytesSaved: 0}, nil
	}
	return transform.ReformatOutput{Output: out, BytesSaved: len(content) - len(out)}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/reformats/ -v`
Expected: PASS, all nine tests.

- [ ] **Step 5: Commit**

```bash
git add internal/reformats/
git commit -m "feat(reformats): JsonMinifier lossless whitespace removal

UseNumber keeps numeric literals exact; SetEscapeHTML(false) stops HTML
expansion inflating the output. Key reorder is an accepted divergence,
made safe by the never-inflate guard.

Refs: hr-47g.26"
```

---

### Task 2: `internal/compress` — log stages 1 to 4

Four pure string-to-string functions. No CCR, no interface, no state — just the transformations, each independently testable.

**Files:**
- Create: `internal/compress/logstages.go`
- Test: `internal/compress/logstages_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (all unexported, used by Task 3):
  - `func stripANSI(s string) string`
  - `func collapseRuns(s string) string`
  - `func dedupWarnings(s string) string`
  - `func dropProgress(s string) string`

- [ ] **Step 1: Write the failing test**

Create `internal/compress/logstages_test.go`:

```go
package compress

import "testing"

func TestStripANSIRemovesColorCodes(t *testing.T) {
	in := "\x1b[31merror\x1b[0m: failed"
	if got, want := stripANSI(in), "error: failed"; got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

func TestStripANSILeavesPlainTextAlone(t *testing.T) {
	in := "error: failed"
	if got := stripANSI(in); got != in {
		t.Errorf("stripANSI = %q, want unchanged %q", got, in)
	}
}

func TestStripANSIHandlesMultiParameterCodes(t *testing.T) {
	in := "\x1b[1;33;40mwarn\x1b[0m"
	if got, want := stripANSI(in), "warn"; got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

func TestCollapseRunsFoldsIdenticalLines(t *testing.T) {
	in := "a\nb\nb\nb\nc"
	want := "a\nb\n... previous line repeated 2 more times\nc"
	if got := collapseRuns(in); got != want {
		t.Errorf("collapseRuns = %q, want %q", got, want)
	}
}

func TestCollapseRunsLeavesSingleLinesAlone(t *testing.T) {
	in := "a\nb\nc"
	if got := collapseRuns(in); got != in {
		t.Errorf("collapseRuns = %q, want unchanged %q", got, in)
	}
}

func TestCollapseRunsHandlesRunAtEnd(t *testing.T) {
	in := "a\nz\nz"
	want := "a\nz\n... previous line repeated 1 more times"
	if got := collapseRuns(in); got != want {
		t.Errorf("collapseRuns = %q, want %q", got, want)
	}
}

func TestDedupWarningsKeepsFirstDropsRepeats(t *testing.T) {
	in := "warning: unused var x\nok\nwarning: unused var x\nwarning: unused var x"
	got := dedupWarnings(in)
	if want := "warning: unused var x\nok\n... 2 more occurrences of 1 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsKeepsDistinctWarnings(t *testing.T) {
	in := "warning: a\nwarning: b"
	if got := dedupWarnings(in); got != in {
		t.Errorf("dedupWarnings = %q, want unchanged %q", got, in)
	}
}

func TestDedupWarningsIsCaseInsensitiveOnTheMarker(t *testing.T) {
	in := "WARNING: dup\nWARNING: dup"
	got := dedupWarnings(in)
	if want := "WARNING: dup\n... 1 more occurrences of 1 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsIgnoresNonWarningLines(t *testing.T) {
	in := "same\nsame\nsame"
	if got := dedupWarnings(in); got != in {
		t.Errorf("dedupWarnings = %q, want unchanged %q", got, in)
	}
}

func TestDedupWarningsIsDeterministic(t *testing.T) {
	in := "warning: z\nwarning: a\nwarning: z\nwarning: a\nwarning: m\nwarning: m"
	first := dedupWarnings(in)
	for i := 0; i < 20; i++ {
		if got := dedupWarnings(in); got != first {
			t.Fatalf("run %d gave %q, first gave %q", i, got, first)
		}
	}
}

func TestDropProgressRemovesOverwrittenLines(t *testing.T) {
	in := "start\ndownloading 10%\rdownloading 90%\ndone"
	if got, want := dropProgress(in), "start\ndone"; got != want {
		t.Errorf("dropProgress = %q, want %q", got, want)
	}
}

func TestDropProgressKeepsCRLFLines(t *testing.T) {
	// A trailing \r is a line terminator, not overwritten output.
	in := "start\r\ndone\r"
	if got, want := dropProgress(in), "start\r\ndone\r"; got != want {
		t.Errorf("dropProgress = %q, want %q", got, want)
	}
}

func TestDropProgressLeavesPlainTextAlone(t *testing.T) {
	in := "a\nb\nc"
	if got := dropProgress(in); got != in {
		t.Errorf("dropProgress = %q, want unchanged %q", got, in)
	}
}

func TestStagesHandleEmptyInput(t *testing.T) {
	for name, fn := range map[string]func(string) string{
		"stripANSI":     stripANSI,
		"collapseRuns":  collapseRuns,
		"dedupWarnings": dedupWarnings,
		"dropProgress":  dropProgress,
	} {
		if got := fn(""); got != "" {
			t.Errorf("%s(\"\") = %q, want empty", name, got)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compress/`
Expected: FAIL — `undefined: stripANSI`.

- [ ] **Step 3: Write the implementation**

Create `internal/compress/logstages.go`:

```go
// Package compress holds information-preserving offload transforms. Each one
// stashes the original in the CCR store before it drops anything, so every
// drop is recoverable through the emitted CacheKey.
package compress

import (
	"fmt"
	"strings"
)

// ansiCSI matches a CSI escape: ESC [ , parameter bytes, intermediate bytes,
// then a final byte in @-~. Written as an explicit character class rather than
// a regexp so the hot path does no regexp work.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f { // parameter bytes
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f { // intermediate bytes
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e { // final byte
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// collapseRuns folds two or more consecutive identical lines into the first
// line plus a repeat count.
func collapseRuns(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		out = append(out, lines[i])
		if n := j - i; n > 1 {
			out = append(out, fmt.Sprintf("... previous line repeated %d more times", n-1))
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// warningBody returns the text after a warning marker, and whether the line is
// a warning at all. Matching is case-insensitive on the marker only; the body
// keeps its original case so distinct warnings stay distinct.
func warningBody(line string) (string, bool) {
	lower := strings.ToLower(line)
	for _, marker := range []string{"warning:", "warn:"} {
		if i := strings.Index(lower, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):]), true
		}
	}
	return "", false
}

// dedupWarnings keeps the first occurrence of each distinct warning in place,
// drops the repeats, and appends one summary line when anything was dropped.
//
// The summary goes at the end rather than in place so that removing a repeat
// never shifts the position of an unrelated line. Iteration is over the line
// slice, never a map, so the result is deterministic.
func dedupWarnings(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	seen := make(map[string]bool, len(lines))
	out := make([]string, 0, len(lines))

	dropped, distinct := 0, 0
	for _, line := range lines {
		body, isWarning := warningBody(line)
		if !isWarning {
			out = append(out, line)
			continue
		}
		if !seen[body] {
			seen[body] = true
			out = append(out, line)
			continue
		}
		dropped++
	}
	if dropped > 0 {
		// Count how many distinct bodies were duplicated, by re-walking the
		// lines rather than the map, to keep the count deterministic.
		dupOf := map[string]int{}
		for _, line := range lines {
			if body, ok := warningBody(line); ok {
				dupOf[body]++
			}
		}
		for _, line := range lines {
			if body, ok := warningBody(line); ok && dupOf[body] > 1 {
				dupOf[body] = 0 // count each distinct body once
				distinct++
			}
		}
		out = append(out, fmt.Sprintf("... %d more occurrences of %d duplicated warning", dropped, distinct))
	}
	return strings.Join(out, "\n")
}

// dropProgress removes lines a terminal would have overwritten: those holding
// a carriage return that is not the line terminator.
func dropProgress(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// A single trailing \r is a CRLF terminator, not overwritten output.
		if strings.Contains(strings.TrimSuffix(line, "\r"), "\r") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compress/ -v`
Expected: PASS, all fifteen tests.

- [ ] **Step 5: Commit**

```bash
git add internal/compress/
git commit -m "feat(compress): log stages 1-4 as pure functions

ANSI strip, identical-run collapse, warning dedup, progress-line drop.
Each is string-to-string with no CCR and no state, so each is testable
alone.

dedupWarnings appends its summary at the end and iterates the line slice
rather than the map, so output position and ordering are deterministic.

Refs: hr-47g.26"
```

---

### Task 3: `internal/compress` — LogCompressor

Stage 5 plus the `OffloadTransform` that runs all five and emits a CCR key.

**Files:**
- Create: `internal/compress/log.go`
- Test: `internal/compress/log_test.go`

**Interfaces:**
- Consumes: `stripANSI`, `collapseRuns`, `dedupWarnings`, `dropProgress` from Task 2; `ccr.ComputeKey`, `ccr.MarkerFor`, `ccr.Store`; `transform.OffloadTransform`, `transform.OffloadOutput`, `transform.CompressionContext`, `transform.ErrInvalidInput`.
- Produces:
  - `type LogConfig struct { HeadLines, TailLines, MinLinesToOffload int }`
  - `func DefaultLogConfig() LogConfig`
  - `type LogCompressor struct { ... }`
  - `func NewLogCompressor(cfg LogConfig) *LogCompressor`
  - `func (c *LogCompressor) Name() string` → `"log_compressor"`
  - `func (c *LogCompressor) AppliesTo() []transform.ContentType`
  - `func (c *LogCompressor) EstimateBloat(content string) float32`
  - `func (c *LogCompressor) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error)`
  - `func (c *LogCompressor) Confidence() float32`
  - `func offloadMiddle(s string, head, tail, min int, marker string) string`

- [ ] **Step 1: Write the failing test**

Create `internal/compress/log_test.go`:

```go
package compress

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// mapStore is a minimal ccr.Store. The real backends need a blank import and a
// TTL; this keeps these tests about the compressor.
type mapStore struct{ m map[string]string }

func newMapStore() *mapStore { return &mapStore{m: map[string]string{}} }

func (s *mapStore) Put(hash, payload string) { s.m[hash] = payload }
func (s *mapStore) Get(hash string) (string, bool) {
	v, ok := s.m[hash]
	return v, ok
}
func (s *mapStore) Len() int { return len(s.m) }

var _ ccr.Store = (*mapStore)(nil)

// manyLines builds n distinct numbered lines, so no stage other than the
// middle offload can shorten it.
func manyLines(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("line %d unique content here", i)
	}
	return strings.Join(parts, "\n")
}

func TestLogCompressorName(t *testing.T) {
	if got := NewLogCompressor(DefaultLogConfig()).Name(); got != "log_compressor" {
		t.Errorf("Name = %q, want log_compressor", got)
	}
}

func TestLogCompressorAppliesToBuildOutput(t *testing.T) {
	types := NewLogCompressor(DefaultLogConfig()).AppliesTo()
	if len(types) != 1 || types[0] != transform.BuildOutput {
		t.Errorf("AppliesTo = %v, want [BuildOutput]", types)
	}
}

func TestDefaultLogConfigValues(t *testing.T) {
	cfg := DefaultLogConfig()
	if cfg.HeadLines != 50 || cfg.TailLines != 50 || cfg.MinLinesToOffload != 200 {
		t.Errorf("DefaultLogConfig = %+v, want {50 50 200}", cfg)
	}
}

func TestNewLogCompressorFillsZeroConfig(t *testing.T) {
	c := NewLogCompressor(LogConfig{})
	if c.cfg != DefaultLogConfig() {
		t.Errorf("cfg = %+v, want the defaults %+v", c.cfg, DefaultLogConfig())
	}
}

func TestOffloadMiddleKeepsHeadAndTail(t *testing.T) {
	in := manyLines(20)
	got := offloadMiddle(in, 3, 2, 10, "<<MARK>>")
	lines := strings.Split(got, "\n")
	if len(lines) != 6 { // 3 head + marker + 2 tail
		t.Fatalf("got %d lines, want 6: %q", len(lines), got)
	}
	if lines[0] != "line 0 unique content here" {
		t.Errorf("first line = %q", lines[0])
	}
	if lines[3] != "<<MARK>>" {
		t.Errorf("marker line = %q, want <<MARK>>", lines[3])
	}
	if lines[5] != "line 19 unique content here" {
		t.Errorf("last line = %q", lines[5])
	}
}

func TestOffloadMiddleBelowThresholdIsNoOp(t *testing.T) {
	in := manyLines(5)
	if got := offloadMiddle(in, 3, 2, 10, "<<MARK>>"); got != in {
		t.Errorf("offloadMiddle shortened input below the threshold")
	}
}

func TestOffloadMiddleNoOpWhenHeadPlusTailCoversInput(t *testing.T) {
	in := manyLines(12)
	// head+tail >= len(lines): there is no middle to remove.
	if got := offloadMiddle(in, 8, 8, 10, "<<MARK>>"); got != in {
		t.Errorf("offloadMiddle changed input with no middle to drop")
	}
}

func TestLogCompressorApplyEmitsResolvableKey(t *testing.T) {
	in := manyLines(500)
	store := newMapStore()

	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.CacheKey == "" {
		t.Fatal("CacheKey is empty")
	}
	got, ok := store.Get(out.CacheKey)
	if !ok {
		t.Fatal("CacheKey does not resolve in the store")
	}
	if got != in {
		t.Error("stored payload is not the exact original")
	}
}

func TestLogCompressorApplyShortensAndReportsSavings(t *testing.T) {
	in := manyLines(500)
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(out.Output) >= len(in) {
		t.Fatalf("output len %d not shorter than input len %d", len(out.Output), len(in))
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Errorf("BytesSaved = %d, want %d", out.BytesSaved, len(in)-len(out.Output))
	}
}

func TestLogCompressorOutputCarriesTheMarker(t *testing.T) {
	in := manyLines(500)
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, ccr.MarkerFor(out.CacheKey)) {
		t.Error("output does not contain the CCR marker for its own key")
	}
}

func TestLogCompressorNeverInflates(t *testing.T) {
	// Short, already-minimal input: no stage can shorten it.
	in := "a\nb\nc"
	store := newMapStore()
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input %q back", out.Output, in)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries; a no-op offload must not write", store.Len())
	}
}

func TestLogCompressorRejectsEmptyInput(t *testing.T) {
	_, err := NewLogCompressor(DefaultLogConfig()).Apply("", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestLogCompressorIsDeterministic(t *testing.T) {
	in := manyLines(500)
	first, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output || next.CacheKey != first.CacheKey {
			t.Fatalf("run %d differs from the first run", i)
		}
	}
}

func TestLogCompressorEstimateBloatGrowsWithLineCount(t *testing.T) {
	c := NewLogCompressor(DefaultLogConfig())
	small := c.EstimateBloat(manyLines(10))
	big := c.EstimateBloat(manyLines(400))
	if !(small < big) {
		t.Errorf("EstimateBloat did not grow: small %v, big %v", small, big)
	}
	if big > 1 {
		t.Errorf("EstimateBloat = %v, must not exceed 1", big)
	}
	if small < 0 {
		t.Errorf("EstimateBloat = %v, must not be negative", small)
	}
}

func TestLogCompressorEstimateBloatIsZeroForOneLine(t *testing.T) {
	if got := NewLogCompressor(DefaultLogConfig()).EstimateBloat("single line"); got != 0 {
		t.Errorf("EstimateBloat = %v, want 0", got)
	}
}

func TestLogCompressorConfidenceIsInRange(t *testing.T) {
	got := NewLogCompressor(DefaultLogConfig()).Confidence()
	if got <= 0 || got > 1 {
		t.Errorf("Confidence = %v, want a value in (0, 1]", got)
	}
}

func TestLogCompressorSatisfiesTheInterface(t *testing.T) {
	var _ transform.OffloadTransform = NewLogCompressor(DefaultLogConfig())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compress/ -run Log`
Expected: FAIL — `undefined: NewLogCompressor`.

- [ ] **Step 3: Write the implementation**

Create `internal/compress/log.go`:

```go
package compress

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// LogConfig holds the compressor's own knobs.
//
// These do NOT live in pipeline.Config: that struct holds the three
// orchestrator gating thresholds, and widening it per compressor would make
// the orchestrator know about every compressor that exists.
type LogConfig struct {
	HeadLines         int
	TailLines         int
	MinLinesToOffload int
}

// DefaultLogConfig returns the documented defaults.
func DefaultLogConfig() LogConfig {
	return LogConfig{HeadLines: 50, TailLines: 50, MinLinesToOffload: 200}
}

// LogCompressor squeezes build and test output through five stages, then
// stashes the original in the CCR store so every drop stays recoverable.
type LogCompressor struct{ cfg LogConfig }

// NewLogCompressor builds a LogCompressor. Any non-positive field falls back
// to its default, so a partly-filled config cannot produce a zero head or tail.
func NewLogCompressor(cfg LogConfig) *LogCompressor {
	d := DefaultLogConfig()
	if cfg.HeadLines <= 0 {
		cfg.HeadLines = d.HeadLines
	}
	if cfg.TailLines <= 0 {
		cfg.TailLines = d.TailLines
	}
	if cfg.MinLinesToOffload <= 0 {
		cfg.MinLinesToOffload = d.MinLinesToOffload
	}
	return &LogCompressor{cfg: cfg}
}

func (c *LogCompressor) Name() string { return "log_compressor" }

func (c *LogCompressor) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.BuildOutput}
}

// Confidence reports how much the pipeline should trust this transform.
func (c *LogCompressor) Confidence() float32 { return 0.8 }

// EstimateBloat is a cheap structural sniff, per the interface contract: it
// counts lines and must NOT run the stages.
func (c *LogCompressor) EstimateBloat(content string) float32 {
	lines := strings.Count(content, "\n") + 1
	if lines <= 1 {
		return 0
	}
	score := float32(lines) / float32(c.cfg.MinLinesToOffload)
	if score > 1 {
		score = 1
	}
	return score
}

// offloadMiddle keeps the first head and last tail lines and replaces the
// middle with marker. It is a no-op below min lines, or when head plus tail
// already covers the input.
func offloadMiddle(s string, head, tail, min int, marker string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < min || head+tail >= len(lines) {
		return s
	}
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, marker)
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// Apply runs the five stages in order.
//
// The key is computed before anything is stored, and the store is written only
// when the result is actually shorter — a no-op offload must not leave an
// entry behind.
func (c *LogCompressor) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	if content == "" {
		return transform.OffloadOutput{}, fmt.Errorf("log_compressor: %w", transform.ErrInvalidInput)
	}

	key := ccr.ComputeKey([]byte(content))

	out := stripANSI(content)
	out = collapseRuns(out)
	out = dedupWarnings(out)
	out = dropProgress(out)
	out = offloadMiddle(out, c.cfg.HeadLines, c.cfg.TailLines, c.cfg.MinLinesToOffload, ccr.MarkerFor(key))

	if len(out) >= len(content) {
		return transform.OffloadOutput{Output: content, BytesSaved: 0}, nil
	}

	store.Put(key, content)
	return transform.OffloadOutput{
		Output:     out,
		BytesSaved: len(content) - len(out),
		CacheKey:   key,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compress/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/compress/
git commit -m "feat(compress): LogCompressor over the five stages

Computes the CCR key before storing and writes the store only when the
result is actually shorter, so a no-op offload leaves nothing behind.

LogConfig is the compressor's own struct rather than fields on
pipeline.Config, which holds orchestrator thresholds only.

Refs: hr-47g.26"
```

---

### Task 4: `internal/compress` — diff parsing and noise scoring

Pure functions again: split a unified diff into hunks, and decide which hunks are noise.

**Files:**
- Create: `internal/compress/diffparse.go`
- Test: `internal/compress/diffparse_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (unexported, used by Task 5):
  - `type hunk struct { file string; header []string; body []string }`
  - `func parseDiff(s string) (preamble []string, hunks []hunk)`
  - `func isLockfile(path string) bool`
  - `func isWhitespaceOnly(h hunk) bool`
  - `func isNoise(h hunk) bool`

- [ ] **Step 1: Write the failing test**

Create `internal/compress/diffparse_test.go`:

```go
package compress

import (
	"strings"
	"testing"
)

const twoFileDiff = `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 func main() {
-	old()
+	new()
 }
diff --git a/go.sum b/go.sum
index 333..444 100644
--- a/go.sum
+++ b/go.sum
@@ -1,2 +1,2 @@
-old/dep v1.0.0 h1:abc=
+new/dep v1.1.0 h1:def=`

func TestParseDiffFindsBothHunks(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(hunks))
	}
}

func TestParseDiffAttributesFiles(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	if hunks[0].file != "main.go" {
		t.Errorf("hunk 0 file = %q, want main.go", hunks[0].file)
	}
	if hunks[1].file != "go.sum" {
		t.Errorf("hunk 1 file = %q, want go.sum", hunks[1].file)
	}
}

func TestParseDiffAttachesFileHeaderToItsHunk(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	joined := strings.Join(hunks[1].header, "\n")
	if !strings.Contains(joined, "diff --git a/go.sum b/go.sum") {
		t.Errorf("hunk 1 header = %q, want the go.sum file header", joined)
	}
}

func TestParseDiffPreambleIsEmptyWhenDiffStartsWithAFile(t *testing.T) {
	preamble, _ := parseDiff(twoFileDiff)
	if len(preamble) != 0 {
		t.Errorf("preamble = %v, want empty", preamble)
	}
}

func TestParseDiffKeepsLeadingTextAsPreamble(t *testing.T) {
	in := "commit abc123\nAuthor: Someone\n\n" + twoFileDiff
	preamble, hunks := parseDiff(in)
	if len(preamble) == 0 || preamble[0] != "commit abc123" {
		t.Errorf("preamble = %v, want it to start with the commit line", preamble)
	}
	if len(hunks) != 2 {
		t.Errorf("got %d hunks, want 2", len(hunks))
	}
}

func TestParseDiffOnNonDiffInputFindsNoHunks(t *testing.T) {
	_, hunks := parseDiff("this is not a diff at all")
	if len(hunks) != 0 {
		t.Errorf("got %d hunks, want 0", len(hunks))
	}
}

func TestParseDiffOnEmptyInputFindsNoHunks(t *testing.T) {
	_, hunks := parseDiff("")
	if len(hunks) != 0 {
		t.Errorf("got %d hunks, want 0", len(hunks))
	}
}

func TestIsLockfileMatchesKnownNames(t *testing.T) {
	for _, p := range []string{
		"go.sum", "vendor/go.sum", "package-lock.json", "a/b/yarn.lock",
		"pnpm-lock.yaml", "Cargo.lock", "poetry.lock", "Gemfile.lock",
		"composer.lock",
	} {
		if !isLockfile(p) {
			t.Errorf("isLockfile(%q) = false, want true", p)
		}
	}
}

func TestIsLockfileRejectsOrdinaryFiles(t *testing.T) {
	for _, p := range []string{"main.go", "go.mod", "lock.go", "src/lockfile.ts"} {
		if isLockfile(p) {
			t.Errorf("isLockfile(%q) = true, want false", p)
		}
	}
}

func TestIsWhitespaceOnlyDetectsReindent(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"-  x := 1",
		"+\tx := 1",
	}}
	if !isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = false, want true for a pure reindent")
	}
}

func TestIsWhitespaceOnlyRejectsRealChange(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"-	old()",
		"+	new()",
	}}
	if isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = true, want false for a real change")
	}
}

func TestIsWhitespaceOnlyIgnoresFileMarkerLines(t *testing.T) {
	// --- and +++ start with - and + but are not content lines.
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"--- a/main.go",
		"+++ b/main.go",
		"-  x := 1",
		"+\tx := 1",
	}}
	if !isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = false; --- and +++ must not count as content")
	}
}

func TestIsWhitespaceOnlyRejectsUnevenAddsAndRemoves(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,3 +1,2 @@",
		"-  x := 1",
		"-  y := 2",
		"+\tx := 1",
	}}
	if isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = true, want false when a line was deleted outright")
	}
}

func TestIsNoiseCatchesLockfileAndWhitespace(t *testing.T) {
	lock := hunk{file: "go.sum", body: []string{"@@ -1 +1 @@", "-a", "+b"}}
	if !isNoise(lock) {
		t.Error("isNoise = false for a lockfile hunk")
	}
	ws := hunk{file: "main.go", body: []string{"@@ -1 +1 @@", "-  x", "+\tx"}}
	if !isNoise(ws) {
		t.Error("isNoise = false for a whitespace-only hunk")
	}
	real := hunk{file: "main.go", body: []string{"@@ -1 +1 @@", "-old()", "+new()"}}
	if isNoise(real) {
		t.Error("isNoise = true for a real code change")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compress/ -run 'ParseDiff|Lockfile|Whitespace|Noise'`
Expected: FAIL — `undefined: parseDiff`.

- [ ] **Step 3: Write the implementation**

Create `internal/compress/diffparse.go`:

```go
package compress

import (
	"path"
	"sort"
	"strings"
)

// hunk is one @@ block plus the file-header lines that introduce it. The
// header travels with the hunk so that dropping a hunk cannot orphan the
// "diff --git" line that named its file.
type hunk struct {
	file   string
	header []string
	body   []string
}

// lockfiles are dependency-manifest files whose diffs are almost always noise
// to a reader: large, mechanical, and derived from another file's change.
var lockfiles = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"Gemfile.lock":      true,
	"composer.lock":     true,
}

func isLockfile(p string) bool { return lockfiles[path.Base(p)] }

// isFileHeader reports whether a line introduces a file rather than content.
func isFileHeader(line string) bool {
	return strings.HasPrefix(line, "diff --git ") ||
		strings.HasPrefix(line, "index ") ||
		strings.HasPrefix(line, "--- ") ||
		strings.HasPrefix(line, "+++ ") ||
		strings.HasPrefix(line, "new file mode ") ||
		strings.HasPrefix(line, "deleted file mode ") ||
		strings.HasPrefix(line, "similarity index ") ||
		strings.HasPrefix(line, "rename ")
}

// fileFromHeader pulls a path out of a "diff --git a/x b/x" or "+++ b/x" line.
func fileFromHeader(line string) (string, bool) {
	if strings.HasPrefix(line, "diff --git ") {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			return strings.TrimPrefix(fields[3], "b/"), true
		}
		return "", false
	}
	if strings.HasPrefix(line, "+++ ") {
		p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		if p == "/dev/null" {
			return "", false
		}
		return strings.TrimPrefix(p, "b/"), true
	}
	return "", false
}

// parseDiff splits a unified diff into the text before the first file header
// and one hunk per @@ block. Input that is not a diff yields no hunks, which
// the caller treats as "nothing to do".
func parseDiff(s string) ([]string, []hunk) {
	if s == "" {
		return nil, nil
	}
	lines := strings.Split(s, "\n")

	var preamble []string
	var hunks []hunk
	var pending []string // file-header lines seen since the last hunk
	var current *hunk
	file := ""
	started := false

	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}

	for _, line := range lines {
		switch {
		case isFileHeader(line):
			flush()
			started = true
			if f, ok := fileFromHeader(line); ok {
				file = f
			}
			pending = append(pending, line)

		case strings.HasPrefix(line, "@@"):
			flush()
			started = true
			current = &hunk{file: file, header: pending, body: []string{line}}
			pending = nil

		default:
			if current != nil {
				current.body = append(current.body, line)
			} else if started {
				pending = append(pending, line)
			} else {
				preamble = append(preamble, line)
			}
		}
	}
	flush()
	return preamble, hunks
}

// contentLines splits a hunk body into its added and removed content,
// excluding the @@ line and the --- / +++ file markers.
func contentLines(h hunk) (added, removed []string) {
	for _, line := range h.body {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "+"):
			added = append(added, line[1:])
		case strings.HasPrefix(line, "-"):
			removed = append(removed, line[1:])
		}
	}
	return added, removed
}

// stripAllWhitespace removes every space, tab, and carriage return so two
// lines differing only in indentation compare equal.
func stripAllWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r':
			return -1
		}
		return r
	}, s)
}

// isWhitespaceOnly reports whether a hunk's added and removed lines are the
// same multiset once whitespace is removed. Sorting makes reordered-but-
// identical sets compare equal and keeps the result deterministic.
func isWhitespaceOnly(h hunk) bool {
	added, removed := contentLines(h)
	if len(added) == 0 || len(added) != len(removed) {
		return false
	}
	a := make([]string, len(added))
	r := make([]string, len(removed))
	for i := range added {
		a[i] = stripAllWhitespace(added[i])
		r[i] = stripAllWhitespace(removed[i])
	}
	sort.Strings(a)
	sort.Strings(r)
	for i := range a {
		if a[i] != r[i] {
			return false
		}
	}
	return true
}

// isNoise reports whether a hunk carries no information a reader needs.
func isNoise(h hunk) bool { return isLockfile(h.file) || isWhitespaceOnly(h) }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compress/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/compress/
git commit -m "feat(compress): unified diff parsing and noise scoring

A hunk carries the file-header lines that introduce it, so dropping a hunk
cannot orphan the diff --git line that named its file.

isWhitespaceOnly sorts both sides before comparing, so a reordered but
otherwise identical set compares equal and the result is deterministic.

Refs: hr-47g.26"
```

---

### Task 5: `internal/compress` — DiffCompressor

The `OffloadTransform` over the parser from Task 4.

**Files:**
- Create: `internal/compress/diff.go`
- Test: `internal/compress/diff_test.go`

**Interfaces:**
- Consumes: `parseDiff`, `isNoise`, `hunk` from Task 4; `mapStore` from Task 3's test file; `ccr.ComputeKey`, `ccr.MarkerFor`, `ccr.Store`; the `transform` interfaces.
- Produces:
  - `type DiffConfig struct { MaxHunks int }`
  - `func DefaultDiffConfig() DiffConfig`
  - `type DiffCompressor struct { ... }`
  - `func NewDiffCompressor(cfg DiffConfig) *DiffCompressor`
  - `func (c *DiffCompressor) Name() string` → `"diff_compressor"`
  - `func (c *DiffCompressor) AppliesTo() []transform.ContentType`
  - `func (c *DiffCompressor) EstimateBloat(content string) float32`
  - `func (c *DiffCompressor) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error)`
  - `func (c *DiffCompressor) Confidence() float32`

- [ ] **Step 1: Write the failing test**

Create `internal/compress/diff_test.go`:

```go
package compress

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// manyHunks builds a diff over n distinct files, each with one real change.
func manyHunks(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "diff --git a/f%d.go b/f%d.go\nindex 111..222 100644\n--- a/f%d.go\n+++ b/f%d.go\n@@ -1,1 +1,1 @@\n-old%d()\n+new%d()\n", i, i, i, i, i, i)
	}
	return b.String()
}

func TestDiffCompressorName(t *testing.T) {
	if got := NewDiffCompressor(DefaultDiffConfig()).Name(); got != "diff_compressor" {
		t.Errorf("Name = %q, want diff_compressor", got)
	}
}

func TestDiffCompressorAppliesToGitDiff(t *testing.T) {
	types := NewDiffCompressor(DefaultDiffConfig()).AppliesTo()
	if len(types) != 1 || types[0] != transform.GitDiff {
		t.Errorf("AppliesTo = %v, want [GitDiff]", types)
	}
}

func TestDefaultDiffConfigValue(t *testing.T) {
	if got := DefaultDiffConfig().MaxHunks; got != 40 {
		t.Errorf("MaxHunks = %d, want 40", got)
	}
}

func TestNewDiffCompressorFillsZeroConfig(t *testing.T) {
	if got := NewDiffCompressor(DiffConfig{}).cfg.MaxHunks; got != 40 {
		t.Errorf("MaxHunks = %d, want the default 40", got)
	}
}

func TestDiffCompressorDropsLockfileHunk(t *testing.T) {
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if strings.Contains(out.Output, "new/dep v1.1.0") {
		t.Error("output still holds the go.sum hunk body")
	}
	if !strings.Contains(out.Output, "new()") {
		t.Error("output lost the real main.go change")
	}
}

func TestDiffCompressorEmitsResolvableKey(t *testing.T) {
	store := newMapStore()
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.CacheKey == "" {
		t.Fatal("CacheKey is empty")
	}
	got, ok := store.Get(out.CacheKey)
	if !ok {
		t.Fatal("CacheKey does not resolve in the store")
	}
	if got != twoFileDiff {
		t.Error("stored payload is not the exact original")
	}
}

func TestDiffCompressorOutputCarriesTheMarker(t *testing.T) {
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, ccr.MarkerFor(out.CacheKey)) {
		t.Error("output does not contain the CCR marker for its own key")
	}
}

func TestDiffCompressorCapsHunkCount(t *testing.T) {
	in := manyHunks(100)
	cfg := DiffConfig{MaxHunks: 5}
	out, err := NewDiffCompressor(cfg).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got := strings.Count(out.Output, "@@ -1,1 +1,1 @@"); got != 5 {
		t.Errorf("kept %d hunks, want 5", got)
	}
	// The first five files survive, in file order.
	if !strings.Contains(out.Output, "new0()") {
		t.Error("output lost the first hunk")
	}
	if strings.Contains(out.Output, "new99()") {
		t.Error("output kept a hunk past the cap")
	}
}

func TestDiffCompressorNeverInflates(t *testing.T) {
	// One tiny real hunk: nothing to drop, and the marker would only add bytes.
	in := "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-a\n+b"
	store := newMapStore()
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input back", out.Output)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries; a no-op offload must not write", store.Len())
	}
}

func TestDiffCompressorSkipsNonDiffInput(t *testing.T) {
	_, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply("this is not a diff", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrSkipped) {
		t.Errorf("err = %v, want ErrSkipped", err)
	}
}

func TestDiffCompressorRejectsEmptyInput(t *testing.T) {
	_, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply("", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestDiffCompressorIsDeterministic(t *testing.T) {
	in := manyHunks(100)
	first, err := NewDiffCompressor(DiffConfig{MaxHunks: 5}).
		Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewDiffCompressor(DiffConfig{MaxHunks: 5}).
			Apply(in, transform.CompressionContext{}, newMapStore())
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output || next.CacheKey != first.CacheKey {
			t.Fatalf("run %d differs from the first run", i)
		}
	}
}

func TestDiffCompressorEstimateBloatGrowsWithHunkCount(t *testing.T) {
	c := NewDiffCompressor(DefaultDiffConfig())
	small := c.EstimateBloat(manyHunks(2))
	big := c.EstimateBloat(manyHunks(80))
	if !(small < big) {
		t.Errorf("EstimateBloat did not grow: small %v, big %v", small, big)
	}
	if big > 1 {
		t.Errorf("EstimateBloat = %v, must not exceed 1", big)
	}
}

func TestDiffCompressorEstimateBloatIsZeroWithoutHunks(t *testing.T) {
	if got := NewDiffCompressor(DefaultDiffConfig()).EstimateBloat("no hunks here"); got != 0 {
		t.Errorf("EstimateBloat = %v, want 0", got)
	}
}

func TestDiffCompressorConfidenceIsInRange(t *testing.T) {
	got := NewDiffCompressor(DefaultDiffConfig()).Confidence()
	if got <= 0 || got > 1 {
		t.Errorf("Confidence = %v, want a value in (0, 1]", got)
	}
}

func TestDiffCompressorSatisfiesTheInterface(t *testing.T) {
	var _ transform.OffloadTransform = NewDiffCompressor(DefaultDiffConfig())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/compress/ -run Diff`
Expected: FAIL — `undefined: NewDiffCompressor`.

- [ ] **Step 3: Write the implementation**

Create `internal/compress/diff.go`:

```go
package compress

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DiffConfig holds the compressor's own knob. See LogConfig for why this is
// not a field on pipeline.Config.
type DiffConfig struct {
	MaxHunks int
}

// DefaultDiffConfig returns the documented default.
func DefaultDiffConfig() DiffConfig { return DiffConfig{MaxHunks: 40} }

// DiffCompressor drops noise hunks from a unified diff and caps how many
// survive. The original goes to the CCR store, so every dropped hunk stays
// recoverable through the emitted key.
type DiffCompressor struct{ cfg DiffConfig }

// NewDiffCompressor builds a DiffCompressor. A non-positive MaxHunks falls
// back to the default, so a partly-filled config cannot drop every hunk.
func NewDiffCompressor(cfg DiffConfig) *DiffCompressor {
	if cfg.MaxHunks <= 0 {
		cfg.MaxHunks = DefaultDiffConfig().MaxHunks
	}
	return &DiffCompressor{cfg: cfg}
}

func (c *DiffCompressor) Name() string { return "diff_compressor" }

func (c *DiffCompressor) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.GitDiff}
}

func (c *DiffCompressor) Confidence() float32 { return 0.75 }

// EstimateBloat is a cheap structural sniff, per the interface contract: it
// counts @@ markers and must NOT parse hunk bodies.
func (c *DiffCompressor) EstimateBloat(content string) float32 {
	hunks := strings.Count(content, "\n@@")
	if strings.HasPrefix(content, "@@") {
		hunks++
	}
	if hunks == 0 {
		return 0
	}
	score := float32(hunks) / float32(c.cfg.MaxHunks)
	if score > 1 {
		score = 1
	}
	return score
}

// Apply drops noise hunks, caps the remainder, and appends the CCR marker.
//
// The key is computed before anything is stored, and the store is written only
// when the result is actually shorter — a no-op offload must not leave an
// entry behind.
func (c *DiffCompressor) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	if content == "" {
		return transform.OffloadOutput{}, fmt.Errorf("diff_compressor: %w", transform.ErrInvalidInput)
	}

	preamble, hunks := parseDiff(content)
	if len(hunks) == 0 {
		return transform.OffloadOutput{}, fmt.Errorf("diff_compressor: no hunks: %w", transform.ErrSkipped)
	}

	key := ccr.ComputeKey([]byte(content))

	out := make([]string, 0, len(preamble)+len(hunks)*4)
	out = append(out, preamble...)

	kept, dropped := 0, 0
	for _, h := range hunks {
		if isNoise(h) || kept >= c.cfg.MaxHunks {
			dropped++
			continue
		}
		kept++
		out = append(out, h.header...)
		out = append(out, h.body...)
	}

	if dropped > 0 {
		out = append(out, fmt.Sprintf("... %d hunk(s) dropped as noise or over the cap", dropped))
		out = append(out, ccr.MarkerFor(key))
	}

	joined := strings.Join(out, "\n")
	if len(joined) >= len(content) {
		return transform.OffloadOutput{Output: content, BytesSaved: 0}, nil
	}

	store.Put(key, content)
	return transform.OffloadOutput{
		Output:     joined,
		BytesSaved: len(content) - len(joined),
		CacheKey:   key,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/compress/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/compress/
git commit -m "feat(compress): DiffCompressor drops noise hunks and caps the rest

Lockfile and whitespace-only hunks go first, then a cap on what survives.
The CCR marker is appended only when something was actually dropped, so a
clean diff passes through without gaining bytes.

Refs: hr-47g.26"
```

---

### Task 6: Prove the pipeline is no longer a passthrough

The three transforms exist but nothing has run them through the orchestrator. This task adds the integration test that would fail if registration or gating were wrong.

**Files:**
- Create: `internal/router/compressors_test.go`
- Modify: `README.md` (add a "Compressors" section)

**Interfaces:**
- Consumes: `reformats.JsonMinifier`, `compress.NewLogCompressor`, `compress.NewDiffCompressor`, `pipeline.NewBuilder`, `router.New`, `ccr.FromConfig`.
- Produces: nothing. Test and documentation only.

> Registration in production code belongs to the entrypoint that builds the
> pipeline — the MCP server and CLI, which are the next plan. This task proves
> the transforms compose correctly; it does not wire them into a binary,
> because there is no binary yet.

- [ ] **Step 1: Write the failing test**

Create `internal/router/compressors_test.go`:

```go
package router_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func newTestRouter() *router.Router {
	p := pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithOffload(compress.NewLogCompressor(compress.DefaultLogConfig())).
		WithOffload(compress.NewDiffCompressor(compress.DefaultDiffConfig())).
		Build()
	return router.New(p)
}

func newTestStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory})
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	return s
}

func TestPipelineCompressesJSON(t *testing.T) {
	in := "[\n  {\"a\": 1},\n  {\"a\": 2},\n  {\"a\": 3}\n]"
	res := newTestRouter().Compress(in, transform.CompressionContext{}, newTestStore(t))

	if res.Output == in {
		t.Fatal("pipeline returned JSON unchanged; it is still a passthrough")
	}
	if res.BytesSaved <= 0 {
		t.Errorf("BytesSaved = %d, want positive", res.BytesSaved)
	}
	if len(res.StepsApplied) == 0 {
		t.Error("StepsApplied is empty")
	}
	if len(res.CacheKeys) != 0 {
		t.Errorf("CacheKeys = %v, want empty; reformats never add keys", res.CacheKeys)
	}
}

func TestPipelineCompressesBuildOutputAndStoresOriginal(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "compiling module number %d of the build\n", i)
	}
	in := strings.TrimSuffix(b.String(), "\n")

	store := newTestStore(t)
	res := newTestRouter().Compress(in, transform.CompressionContext{}, store)

	if res.Output == in {
		t.Fatal("pipeline returned build output unchanged")
	}
	if len(res.CacheKeys) == 0 {
		t.Fatal("no CacheKeys; an offload must publish its key")
	}
	got, ok := store.Get(res.CacheKeys[0])
	if !ok {
		t.Fatal("CacheKey does not resolve in the store")
	}
	if got != in {
		t.Error("stored payload is not the exact original")
	}
}

func TestPipelineNeverInflatesAnyContent(t *testing.T) {
	// The invariant that matters most: no input may come back longer.
	inputs := map[string]string{
		"tiny json":  `[1,2]`,
		"plain text": "just a sentence of prose",
		"short log":  "building\ndone",
		"short diff": "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-a\n+b",
		"empty":      "",
	}
	r := newTestRouter()
	for name, in := range inputs {
		res := r.Compress(in, transform.CompressionContext{}, newTestStore(t))
		if len(res.Output) > len(in) {
			t.Errorf("%s: output len %d exceeds input len %d", name, len(res.Output), len(in))
		}
	}
}

func TestPipelineIsDeterministic(t *testing.T) {
	in := "[\n  {\"z\": 1, \"a\": 2},\n  {\"z\": 3, \"a\": 4}\n]"
	r := newTestRouter()

	first := r.Compress(in, transform.CompressionContext{}, newTestStore(t))
	for i := 0; i < 20; i++ {
		next := r.Compress(in, transform.CompressionContext{}, newTestStore(t))
		if next.Output != first.Output {
			t.Fatalf("run %d gave %q, first gave %q", i, next.Output, first.Output)
		}
	}
}

func TestPipelineSurvivesMalformedInput(t *testing.T) {
	// A transform that errors must be skipped, not propagated, and never panic.
	r := newTestRouter()
	for _, in := range []string{
		"[{unclosed",
		"@@ not really a diff @@",
		"\x00\x01\x02",
	} {
		res := r.Compress(in, transform.CompressionContext{}, newTestStore(t))
		if len(res.Output) > len(in) {
			t.Errorf("malformed input %q grew", in)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/router/ -run Pipeline`
Expected: FAIL — the packages `compress` and `reformats` do not resolve until Tasks 1 to 5 are committed. If those are committed, expect PASS; this task's test is the proof, not new behaviour.

- [ ] **Step 3: Fix anything the integration test exposes**

If a test fails, the defect is in the transform, not the test. Fix the transform and say which one in your notes. Do not weaken the test.

The likely failure is `TestPipelineNeverInflatesAnyContent` on the `"empty"` case: `Compress("")` runs the detector on an empty string. Confirm the detector's classification and that every transform returns a sentinel error rather than growing the input.

- [ ] **Step 4: Run the full suite**

Run: `CGO_ENABLED=0 go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Document the compressors in the README**

Append this section to `README.md`:

```markdown
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
```

- [ ] **Step 6: Run every gate**

Run:
```bash
CGO_ENABLED=0 go build ./... && \
CGO_ENABLED=0 go test -race ./... && \
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && \
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && \
gofmt -l . && go vet ./... && \
go mod tidy && git diff --exit-code go.mod go.sum
```
Expected: every command succeeds, `gofmt -l` prints nothing, no `go.mod` diff.

- [ ] **Step 7: Commit**

```bash
git add internal/router/compressors_test.go README.md
git commit -m "test(router): prove the pipeline is no longer a passthrough

Integration test over the real orchestrator: JSON shrinks, build output
offloads with a resolvable key, nothing inflates, output is deterministic,
and malformed input is skipped rather than propagated.

Registration in production code belongs to the entrypoint that builds the
pipeline, which is the next plan.

Refs: hr-47g.26"
```

---

## What this plan does not do

Named so a reviewer does not look for them:

- **No registration in a binary.** There is no `cmd/headroom`. The entrypoint that builds a pipeline with these transforms is the next plan (v0.1 items 11 and 10).
- **No SearchCompressor, LogTemplate, tagprotect, toolpairs, or adaptive sizer.** Deferred, addendum section 2.
- **No SmartCrusher.** v0.1 item 5, its own plan.
- **No upstream output parity.** Clean-room designs; addendum section 1.
