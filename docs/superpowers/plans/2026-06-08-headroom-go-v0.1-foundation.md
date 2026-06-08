# headroom-go v0.1 — Plan 1: Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the `headroom-go` repo and the compression *spine* — core interfaces, CCR store, tokenizer, content detector, pipeline orchestrator, and router — compiling and fully tested, with no real compressors yet (the pipeline runs as faithful passthrough until transforms are registered in Plan 2).

**Architecture:** A `transform` package defines the `ReformatTransform`/`OffloadTransform` interfaces and `ContentType`. `ccr` provides the reversible-compression store (BLAKE3-keyed) and markers. `tokenizer` counts tokens (estimator + tiktoken). `detect` classifies content into 7 types. `pipeline` orchestrates reformats-then-offloads with exact gating. `router` ties detect→pipeline together. No import cycles: `ccr` and `tokenizer` depend only on stdlib + libs; `transform` imports `ccr`; `detect` imports `transform`; `pipeline` imports `transform`+`ccr`; `router` imports `detect`+`pipeline`.

**Tech Stack:** Go 1.25, `lukechampine.com/blake3`, `modernc.org/sqlite` (pure-Go), `github.com/BurntSushi/toml`, `github.com/pkoukk/tiktoken-go` + `github.com/pkoukk/tiktoken-go-loader`. cgo-free.

**Spec:** `docs/superpowers/specs/2026-06-08-headroom-go-core-design.md` (§5 interfaces are the canonical contract).

---

## File Structure

```
go.mod, go.sum
LICENSE                         # Apache-2.0
NOTICE                          # attribution to chopratejas/headroom
README.md                       # stub
GOALS.md                        # objective/done/follow-ups
CLAUDE.md                       # global guidelines block
.github/workflows/ci.yml        # build/vet/test
internal/transform/transform.go        + transform_test.go
internal/ccr/key.go             internal/ccr/marker.go      internal/ccr/store.go
internal/ccr/backends/inmemory.go       internal/ccr/backends/sqlite.go
internal/ccr/key_test.go  marker_test.go  backends/inmemory_test.go  backends/sqlite_test.go
internal/tokenizer/estimator.go  tiktoken.go  registry.go   + *_test.go
internal/detect/detect.go              + detect_test.go
internal/pipeline/config.go  pipeline.go  pipeline.toml      + pipeline_test.go
internal/router/router.go              + router_test.go
```

---

## Task 0: Project tracking (beads)

**Files:** none (tooling). Run once at execution start, in the repo root.

- [ ] **Step 1: Initialize beads via the setup-beads skill**

Invoke the `setup-beads` skill (Dolt backend) in `/Users/christopherdobbyn/work/dobbo-ca/headroom-go`. Follow its prompts to create the `bd` tracker.

- [ ] **Step 2: Create the epic + task hierarchy**

Create one epic per sub-project and child issues for this plan's tasks:

```bash
bd create epic "headroom-go core" --description "Clean-room Go port of chopratejas/headroom (proxy+MCP+CLI)"
bd create epic "kompress-go runtime" --description "ONNX dual-head Kompress inference in Go (sub-project #2)"
bd create epic "kompress training" --description "LLMLingua-2 distillation on our traces (sub-project #3)"
bd create epic "token-reduction guide" --description "rtk + headroom + graphify workflow doc (sub-project #4)"
# Under the core epic, one issue per Plan 1 task:
bd create task "Foundation: repo scaffold"        --parent <core-epic-id>
bd create task "Foundation: transform interfaces" --parent <core-epic-id>
bd create task "Foundation: ccr store + markers"  --parent <core-epic-id>
bd create task "Foundation: tokenizer"            --parent <core-epic-id>
bd create task "Foundation: content detector"     --parent <core-epic-id>
bd create task "Foundation: pipeline orchestrator"--parent <core-epic-id>
bd create task "Foundation: router + integration" --parent <core-epic-id>
```

- [ ] **Step 3: Verify**

Run: `bd list`
Expected: 4 epics + 7 foundation tasks listed.

---

## Task 1: Repo scaffold

**Files:**
- Create: `go.mod`, `LICENSE`, `NOTICE`, `README.md`, `GOALS.md`, `CLAUDE.md`, `.github/workflows/ci.yml`

- [ ] **Step 1: Create the Go module**

Run (from repo root):
```bash
go mod init github.com/dobbo-ca/headroom-go
```
Then edit `go.mod` to pin Go 1.25:
```
module github.com/dobbo-ca/headroom-go

go 1.25.0
```

- [ ] **Step 2: Add LICENSE (Apache-2.0)**

Run:
```bash
curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE
```
Expected: `LICENSE` exists, begins with "Apache License / Version 2.0".

- [ ] **Step 3: Add NOTICE attribution**

Create `NOTICE`:
```
# Attribution

headroom-go is a clean-room Go reimplementation of headroom by Tejas Chopra.

- Original project: https://github.com/chopratejas/headroom (Python/Rust, Apache-2.0)
- This port: https://github.com/dobbo-ca/headroom-go (Go, Apache-2.0)

headroom is an LLM context-compression layer: it compresses tool outputs, logs,
diffs, search results, and RAG chunks before they reach the model. This port
re-implements the compression pipeline and the proxy/MCP/CLI surfaces in Go.

It is a clean-room reimplementation of the original's pipeline and behavior,
not a line-by-line translation, and deliberately does NOT reproduce upstream's
cross-implementation byte-parity (Python float-formatting quirks). The CCR
marker formats and compression contracts are kept self-consistent within this
implementation.

The original Apache-2.0 copyright is preserved in LICENSE.
```

- [ ] **Step 4: Add README, GOALS, CLAUDE.md stubs**

Create `README.md`:
```markdown
# headroom-go

Clean-room Go port of [headroom](https://github.com/chopratejas/headroom) — an
LLM context-compression layer. Compress tool outputs, logs, diffs, and search
results before they reach the model: 60–95% fewer tokens, same answers.

Status: v0.1 in progress (compression engine + MCP server). See
`docs/superpowers/specs/` and `docs/superpowers/plans/`.

## Install

    go install github.com/dobbo-ca/headroom-go/cmd/headroom@latest

## License

Apache-2.0. See `LICENSE` and `NOTICE`.
```

Create `GOALS.md`:
```markdown
# GOALS

Reference for future sessions. Tracks the objective, what's done, what's left.

## Objective

Clean-room Go port of headroom (chopratejas/headroom) as dobbo-ca/headroom-go:
an LLM context-compression layer exposed as a drop-in proxy, an MCP server, and
a CLI wrapper, cutting tokens 60–95% while preserving answers.

## Done

- [ ] (Plan 1) Foundation: transform interfaces, CCR, tokenizer, detector, pipeline, router.

## Follow-ups

See the spec's §8 phasing and §10 parity checklist.

## Out of scope (deferred to follow-ups / kompress-go)

ML prose compression (ONNX Kompress), Python framework integrations, byte-parity
with upstream, Bedrock/Vertex/WebSocket transports.
```

Create `CLAUDE.md`:
```markdown
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
```

- [ ] **Step 5: Add CI workflow**

Create `.github/workflows/ci.yml`:
```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.25"
      - name: Build
        run: go build ./...
      - name: Vet
        run: go vet ./...
      - name: Test
        run: go test ./...
```

- [ ] **Step 6: Verify it builds and commit**

Run:
```bash
go build ./... && go vet ./...
```
Expected: no output (success; no packages yet is fine).
```bash
git add go.mod LICENSE NOTICE README.md GOALS.md CLAUDE.md .github/workflows/ci.yml
git commit -m "chore: scaffold headroom-go repo (module, license, NOTICE, CI)"
```

---

## Task 2: transform package (interfaces + ContentType)

**Files:**
- Create: `internal/transform/transform.go`
- Test: `internal/transform/transform_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/transform/transform_test.go`:
```go
package transform

import (
	"errors"
	"testing"
)

func TestContentTypeString(t *testing.T) {
	cases := map[ContentType]string{
		JsonArray:     "json_array",
		SourceCode:    "source_code",
		SearchResults: "search",
		BuildOutput:   "build",
		GitDiff:       "diff",
		Html:          "html",
		PlainText:     "text",
	}
	for ct, want := range cases {
		if got := ct.String(); got != want {
			t.Errorf("ContentType(%d).String() = %q, want %q", ct, got, want)
		}
	}
}

func TestContentTypeStringUnknown(t *testing.T) {
	if got := ContentType(99).String(); got != "text" {
		t.Errorf("unknown ContentType should fall back to %q, got %q", "text", got)
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	if errors.Is(ErrSkipped, ErrInvalidInput) || errors.Is(ErrInternal, ErrSkipped) {
		t.Fatal("sentinel errors must be distinct")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/`
Expected: FAIL — `undefined: ContentType` (package has no source yet).

- [ ] **Step 3: Write minimal implementation**

Create `internal/transform/transform.go`:
```go
// Package transform defines the content types and the Reformat/Offload
// transform interfaces that the compression pipeline composes.
package transform

import (
	"errors"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// ContentType is the routing key produced by the detector.
type ContentType int

const (
	JsonArray ContentType = iota
	SourceCode
	SearchResults
	BuildOutput
	GitDiff
	Html
	PlainText
)

// String returns the stable tag used in config and logs. Unknown values fall
// back to "text" so routing never panics.
func (c ContentType) String() string {
	switch c {
	case JsonArray:
		return "json_array"
	case SourceCode:
		return "source_code"
	case SearchResults:
		return "search"
	case BuildOutput:
		return "build"
	case GitDiff:
		return "diff"
	case Html:
		return "html"
	default:
		return "text"
	}
}

// CompressionContext carries per-call relevance and budget hints.
type CompressionContext struct {
	Query       string
	TokenBudget *int
}

// Sentinel errors. ALL mean "skip this transform, continue the pipeline,
// never panic". Transforms wrap these with %w.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrSkipped      = errors.New("skipped")
	ErrInternal     = errors.New("internal")
)

// ReformatOutput is the result of a lossless transform. Output is semantically
// equivalent to the input; there is no CCR backing.
type ReformatOutput struct {
	Output     string
	BytesSaved int
}

// OffloadOutput is the result of an information-preserving transform. Output is
// a subset of the input; the original is stashed in a CCR store under CacheKey.
type OffloadOutput struct {
	Output     string
	BytesSaved int
	CacheKey   string
}

// ReformatTransform packs content denser without dropping information. Runs
// first in the pipeline (surviving bytes must round-trip semantically).
type ReformatTransform interface {
	Name() string
	AppliesTo() []ContentType
	Apply(content string) (ReformatOutput, error)
}

// OffloadTransform subsets content and stashes the original in the store.
// Apply runs only after EstimateBloat clears the pipeline's threshold and MUST
// emit a CacheKey that resolves in the provided store.
type OffloadTransform interface {
	Name() string
	AppliesTo() []ContentType
	EstimateBloat(content string) float32 // 0..1, cheap structural sniff, NO full pass
	Apply(content string, ctx CompressionContext, store ccr.Store) (OffloadOutput, error)
	Confidence() float32
}
```

> Note: `transform` imports `ccr`, so Task 3 (`ccr`) must compile first. If executing strictly top-to-bottom, this step will not build until Task 3's `ccr.Store` exists. Implement Task 3 before re-running this test, or temporarily stub `store any` — the canonical version imports `ccr.Store`.

- [ ] **Step 4: Run test to verify it passes (after Task 3 lands)**

Run: `go test ./internal/transform/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/transform/
git commit -m "feat(transform): content types and reformat/offload interfaces"
```

---

## Task 3: ccr package (key, markers, store, backends)

**Files:**
- Create: `internal/ccr/key.go`, `internal/ccr/marker.go`, `internal/ccr/store.go`, `internal/ccr/backends/inmemory.go`, `internal/ccr/backends/sqlite.go`
- Test: `internal/ccr/key_test.go`, `internal/ccr/marker_test.go`, `internal/ccr/backends/inmemory_test.go`, `internal/ccr/backends/sqlite_test.go`

- [ ] **Step 1: Write the failing test for the key**

Create `internal/ccr/key_test.go`:
```go
package ccr

import "testing"

func TestComputeKeyDeterministicAnd24Hex(t *testing.T) {
	a := ComputeKey([]byte("hello world"))
	b := ComputeKey([]byte("hello world"))
	if a != b {
		t.Fatalf("ComputeKey not deterministic: %q != %q", a, b)
	}
	if len(a) != 24 {
		t.Fatalf("key length = %d, want 24 hex chars", len(a))
	}
	for _, r := range a {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("key has non-lowercase-hex rune %q in %q", r, a)
		}
	}
	if ComputeKey([]byte("different")) == a {
		t.Fatal("distinct inputs produced the same key")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ccr/`
Expected: FAIL — `undefined: ComputeKey`.

- [ ] **Step 3: Implement the key**

Create `internal/ccr/key.go`:
```go
// Package ccr implements reversible compression: originals are stashed in a
// Store under a short content key, and a marker is left in the compressed text
// so headroom_retrieve can recover the original on demand.
package ccr

import (
	"encoding/hex"

	"lukechampine.com/blake3"
)

// ComputeKey returns the first 24 lowercase hex chars (96 bits) of the BLAKE3
// hash of payload. Deterministic; collision-safe for CCR's working-set sizes.
func ComputeKey(payload []byte) string {
	sum := blake3.Sum256(payload)
	return hex.EncodeToString(sum[:12])
}
```
Then add the dependency:
```bash
go get lukechampine.com/blake3
```

- [ ] **Step 4: Run the key test**

Run: `go test ./internal/ccr/ -run TestComputeKey`
Expected: PASS.

- [ ] **Step 5: Write the failing marker test**

Create `internal/ccr/marker_test.go`:
```go
package ccr

import "testing"

func TestCanonicalMarkerRoundTrip(t *testing.T) {
	h := "0123456789abcdef01234567"
	m := MarkerFor(h)
	if m != "<<ccr:"+h+">>" {
		t.Fatalf("MarkerFor = %q", m)
	}
	got, ok := ParseMarker(m)
	if !ok || got != h {
		t.Fatalf("ParseMarker(%q) = %q,%v", m, got, ok)
	}
}

func TestParseMarkerRejectsNonMarker(t *testing.T) {
	if _, ok := ParseMarker("not a marker"); ok {
		t.Fatal("ParseMarker accepted non-marker text")
	}
	if _, ok := ParseMarker("<<ccr:short>>"); ok {
		t.Fatal("ParseMarker accepted wrong-length hash")
	}
}

func TestCellAndLossyMarkersDistinct(t *testing.T) {
	h := "0123456789abcdef01234567"
	if MarkerForCell(h, "json", 42) != "<<ccr:"+h+",json,42>>" {
		t.Fatalf("cell marker = %q", MarkerForCell(h, "json", 42))
	}
	if MarkerForLossy(h, 7) != "<<ccr:"+h+" 7_rows_offloaded>>" {
		t.Fatalf("lossy marker = %q", MarkerForLossy(h, 7))
	}
}
```

- [ ] **Step 6: Implement markers**

Create `internal/ccr/marker.go`:
```go
package ccr

import (
	"fmt"
	"regexp"
)

// Three marker surfaces exist and are intentionally NOT unified:
//   canonical: <<ccr:HASH>>                       (live-zone block offload)
//   cell:      <<ccr:HASH,KIND,SIZE>>             (compaction opaque cell)
//   lossy:     <<ccr:HASH N_rows_offloaded>>      (lossy row drop)

const markerPrefix = "<<ccr:"
const markerSuffix = ">>"

var canonicalRe = regexp.MustCompile(`^<<ccr:([0-9a-f]{24})>>$`)

// MarkerFor builds the canonical live-zone marker.
func MarkerFor(hash string) string { return markerPrefix + hash + markerSuffix }

// MarkerForCell builds a compaction opaque-cell marker.
func MarkerForCell(hash, kind string, size int) string {
	return fmt.Sprintf("%s%s,%s,%d%s", markerPrefix, hash, kind, size, markerSuffix)
}

// MarkerForLossy builds a lossy row-drop marker.
func MarkerForLossy(hash string, rows int) string {
	return fmt.Sprintf("%s%s %d_rows_offloaded%s", markerPrefix, hash, rows, markerSuffix)
}

// ParseMarker extracts the hash from a canonical marker. Cell/lossy markers are
// parsed by their own consumers; this returns ok=false for them.
func ParseMarker(s string) (hash string, ok bool) {
	m := canonicalRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}
```

- [ ] **Step 7: Run the marker test**

Run: `go test ./internal/ccr/ -run Marker`
Expected: PASS.

- [ ] **Step 8: Define the Store interface and factory (test)**

Create `internal/ccr/store.go`:
```go
package ccr

import (
	"fmt"
	"time"
)

// Store holds CCR originals keyed by ComputeKey output.
type Store interface {
	Put(hash, payload string)
	Get(hash string) (string, bool)
	Len() int
}

const (
	DefaultCapacity = 1000
	DefaultTTL      = 5 * time.Minute
)

// BackendKind selects a Store implementation.
type BackendKind int

const (
	InMemory BackendKind = iota
	SQLite
)

// BackendConfig configures a Store. Capacity applies to InMemory (FIFO cap);
// SQLite is TTL-only (no capacity cap) — preserve this asymmetry.
type BackendConfig struct {
	Kind       BackendKind
	Capacity   int
	TTLSeconds uint64
	Path       string // SQLite file path
}

// newInMemory and newSQLite are wired by the backends package via Register to
// avoid an import cycle (ccr/backends imports ccr, not vice-versa).
var (
	newInMemory func(capacity int, ttl time.Duration) Store
	newSQLite   func(path string, ttl time.Duration) (Store, error)
)

// RegisterInMemory and RegisterSQLite are called from backends' init().
func RegisterInMemory(f func(capacity int, ttl time.Duration) Store)        { newInMemory = f }
func RegisterSQLite(f func(path string, ttl time.Duration) (Store, error))  { newSQLite = f }

// FromConfig builds a Store. Import the backends package (blank import) before
// calling so the constructors are registered.
func FromConfig(cfg BackendConfig) (Store, error) {
	ttl := DefaultTTL
	if cfg.TTLSeconds > 0 {
		ttl = time.Duration(cfg.TTLSeconds) * time.Second
	}
	switch cfg.Kind {
	case InMemory:
		if newInMemory == nil {
			return nil, fmt.Errorf("ccr: in-memory backend not registered (blank-import internal/ccr/backends)")
		}
		cap := cfg.Capacity
		if cap <= 0 {
			cap = DefaultCapacity
		}
		return newInMemory(cap, ttl), nil
	case SQLite:
		if newSQLite == nil {
			return nil, fmt.Errorf("ccr: sqlite backend not registered (blank-import internal/ccr/backends)")
		}
		return newSQLite(cfg.Path, ttl)
	default:
		return nil, fmt.Errorf("ccr: unknown backend kind %d", cfg.Kind)
	}
}
```

- [ ] **Step 9: Write the in-memory backend test**

Create `internal/ccr/backends/inmemory_test.go`:
```go
package backends

import (
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func TestInMemoryPutGet(t *testing.T) {
	s := newInMemory(2, time.Minute)
	s.Put("a", "alpha")
	if v, ok := s.Get("a"); !ok || v != "alpha" {
		t.Fatalf("Get(a) = %q,%v", v, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("Get(missing) should be false")
	}
}

func TestInMemoryFIFOEviction(t *testing.T) {
	s := newInMemory(2, time.Minute)
	s.Put("a", "1")
	s.Put("b", "2")
	s.Put("c", "3") // evicts "a"
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected oldest entry evicted")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("newest entry should be present")
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
}

func TestInMemoryTTLExpiry(t *testing.T) {
	s := newInMemory(10, 10*time.Millisecond)
	s.Put("a", "1")
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected entry expired by TTL")
	}
}

func TestInMemoryRegisteredFactory(t *testing.T) {
	st, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 4})
	if err != nil {
		t.Fatal(err)
	}
	st.Put("k", "v")
	if v, ok := st.Get("k"); !ok || v != "v" {
		t.Fatalf("factory store Get = %q,%v", v, ok)
	}
}
```

- [ ] **Step 10: Run it to verify it fails**

Run: `go test ./internal/ccr/backends/`
Expected: FAIL — `undefined: newInMemory`.

- [ ] **Step 11: Implement the in-memory backend**

Create `internal/ccr/backends/inmemory.go`:
```go
// Package backends provides CCR Store implementations. Blank-import this package
// to register them with ccr.FromConfig.
package backends

import (
	"container/list"
	"sync"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func init() { ccr.RegisterInMemory(func(c int, ttl time.Duration) ccr.Store { return newInMemory(c, ttl) }) }

type entry struct {
	hash    string
	payload string
	expires time.Time
}

type inMemory struct {
	mu       sync.Mutex
	cap      int
	ttl      time.Duration
	items    map[string]*list.Element // hash -> element holding *entry
	order    *list.List               // front = oldest
}

func newInMemory(capacity int, ttl time.Duration) *inMemory {
	return &inMemory{cap: capacity, ttl: ttl, items: make(map[string]*list.Element), order: list.New()}
}

func (m *inMemory) Put(hash, payload string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[hash]; ok {
		el.Value.(*entry).payload = payload
		el.Value.(*entry).expires = m.deadline()
		return
	}
	el := m.order.PushBack(&entry{hash: hash, payload: payload, expires: m.deadline()})
	m.items[hash] = el
	for m.order.Len() > m.cap {
		oldest := m.order.Front()
		m.order.Remove(oldest)
		delete(m.items, oldest.Value.(*entry).hash)
	}
}

func (m *inMemory) Get(hash string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[hash]
	if !ok {
		return "", false
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && timeNow().After(e.expires) {
		m.order.Remove(el)
		delete(m.items, hash)
		return "", false
	}
	return e.payload, true
}

func (m *inMemory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order.Len()
}

func (m *inMemory) deadline() time.Time {
	if m.ttl <= 0 {
		return time.Time{}
	}
	return timeNow().Add(m.ttl)
}

// timeNow is a package var so tests stay deterministic if needed; it is NOT on
// the compression path (CCR storage is side-channel, not subject to I4).
var timeNow = time.Now
```

- [ ] **Step 12: Run the in-memory tests**

Run: `go test ./internal/ccr/backends/ -run InMemory`
Expected: PASS (all four).

- [ ] **Step 13: Write the SQLite backend test**

Create `internal/ccr/backends/sqlite_test.go`:
```go
package backends

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func TestSQLitePutGet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	s, err := newSQLite(p, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.Put("a", "alpha")
	if v, ok := s.Get("a"); !ok || v != "alpha" {
		t.Fatalf("Get(a) = %q,%v", v, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("Get(missing) should be false")
	}
}

func TestSQLiteTTLExpiry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	s, err := newSQLite(p, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	s.Put("a", "1")
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected entry expired by TTL")
	}
}

func TestSQLiteRegisteredFactory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	st, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.SQLite, Path: p})
	if err != nil {
		t.Fatal(err)
	}
	st.Put("k", "v")
	if v, ok := st.Get("k"); !ok || v != "v" {
		t.Fatalf("factory store Get = %q,%v", v, ok)
	}
}
```

- [ ] **Step 14: Run it to verify it fails**

Run: `go test ./internal/ccr/backends/ -run SQLite`
Expected: FAIL — `undefined: newSQLite`.

- [ ] **Step 15: Implement the SQLite backend**

Add the dependency:
```bash
go get modernc.org/sqlite
```
Create `internal/ccr/backends/sqlite.go`:
```go
package backends

import (
	"database/sql"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "modernc.org/sqlite"
)

func init() {
	ccr.RegisterSQLite(func(path string, ttl time.Duration) (ccr.Store, error) { return newSQLite(path, ttl) })
}

type sqliteStore struct {
	db  *sql.DB
	ttl time.Duration
}

func newSQLite(path string, ttl time.Duration) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ccr (
		hash TEXT PRIMARY KEY,
		payload TEXT NOT NULL,
		expires_unix_ns INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db, ttl: ttl}, nil
}

func (s *sqliteStore) Put(hash, payload string) {
	var exp int64
	if s.ttl > 0 {
		exp = timeNow().Add(s.ttl).UnixNano()
	}
	_, _ = s.db.Exec(
		`INSERT INTO ccr(hash,payload,expires_unix_ns) VALUES(?,?,?)
		 ON CONFLICT(hash) DO UPDATE SET payload=excluded.payload, expires_unix_ns=excluded.expires_unix_ns`,
		hash, payload, exp,
	)
}

func (s *sqliteStore) Get(hash string) (string, bool) {
	var payload string
	var exp int64
	err := s.db.QueryRow(`SELECT payload, expires_unix_ns FROM ccr WHERE hash=?`, hash).Scan(&payload, &exp)
	if err != nil {
		return "", false
	}
	if exp != 0 && timeNow().UnixNano() > exp {
		_, _ = s.db.Exec(`DELETE FROM ccr WHERE hash=?`, hash)
		return "", false
	}
	return payload, true
}

func (s *sqliteStore) Len() int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ccr`).Scan(&n); err != nil {
		return 0
	}
	return n
}
```

- [ ] **Step 16: Run all ccr tests**

Run: `go test ./internal/ccr/...`
Expected: PASS. Now also re-run Task 2:
Run: `go test ./internal/transform/`
Expected: PASS (now that `ccr.Store` exists).

- [ ] **Step 17: Commit**

```bash
git add internal/ccr/ internal/transform/ go.mod go.sum
git commit -m "feat(ccr): BLAKE3 key, markers, in-memory + sqlite stores; transform iface"
```

---

## Task 4: tokenizer package

**Files:**
- Create: `internal/tokenizer/estimator.go`, `internal/tokenizer/tiktoken.go`, `internal/tokenizer/registry.go`
- Test: `internal/tokenizer/estimator_test.go`, `internal/tokenizer/registry_test.go`

- [ ] **Step 1: Write the failing estimator test**

Create `internal/tokenizer/estimator_test.go`:
```go
package tokenizer

import "testing"

func TestEstimatorRoundHalfUpRunes(t *testing.T) {
	// cpt = 4.0. "abcd" = 4 runes -> 1 token. "abcde" = 5 runes -> round(1.25)=1.
	// "abcdef" = 6 runes -> round(1.5) = 2 (round half up).
	c := EstimatingCounter{CharsPerToken: 4.0}
	cases := map[string]int{"": 1, "a": 1, "abcd": 1, "abcde": 1, "abcdef": 2, "abcdefgh": 2}
	for in, want := range cases {
		if got := c.CountText(in); got != want {
			t.Errorf("CountText(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestEstimatorCountsRunesNotBytes(t *testing.T) {
	c := EstimatingCounter{CharsPerToken: 4.0}
	// "é" is 2 bytes but 1 rune; 4 of them = 4 runes -> 1 token, not 2.
	if got := c.CountText("éééé"); got != 1 {
		t.Errorf("CountText(4 runes) = %d, want 1 (rune-based)", got)
	}
}

func TestEstimatorBackend(t *testing.T) {
	if (EstimatingCounter{CharsPerToken: 4}).Backend() != BackendEstimation {
		t.Fatal("estimator must report BackendEstimation")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tokenizer/ -run Estimator`
Expected: FAIL — `undefined: EstimatingCounter`.

- [ ] **Step 3: Implement the estimator + interface**

Create `internal/tokenizer/estimator.go`:
```go
// Package tokenizer counts tokens for compression ratio decisions. It ships a
// cheap rune-based estimator and a tiktoken backend; the HF backend is a
// follow-up. The estimator alone covers Claude (the primary target model).
package tokenizer

import "unicode/utf8"

// Backend identifies which counting strategy a Tokenizer uses.
type Backend int

const (
	BackendTiktoken Backend = iota
	BackendHuggingFace
	BackendEstimation
)

// Tokenizer counts tokens in text.
type Tokenizer interface {
	CountText(text string) int
	Backend() Backend
}

// EstimatingCounter approximates token count as runes / CharsPerToken, rounded
// half-up, with a floor of 1. Rune-based (not bytes) so multibyte text is not
// over-counted. Deterministic and dependency-free.
type EstimatingCounter struct {
	CharsPerToken float64
}

const defaultCharsPerToken = 4.0

func (e EstimatingCounter) CountText(text string) int {
	cpt := e.CharsPerToken
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}
	runes := utf8.RuneCountInString(text)
	n := int(float64(runes)/cpt + 0.5) // round half up
	if n < 1 {
		return 1
	}
	return n
}

func (e EstimatingCounter) Backend() Backend { return BackendEstimation }
```

- [ ] **Step 4: Run the estimator test**

Run: `go test ./internal/tokenizer/ -run Estimator`
Expected: PASS.

- [ ] **Step 5: Implement the tiktoken backend (offline)**

Add dependencies:
```bash
go get github.com/pkoukk/tiktoken-go
go get github.com/pkoukk/tiktoken-go-loader
```
Create `internal/tokenizer/tiktoken.go`:
```go
package tokenizer

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"
)

var offlineOnce sync.Once

// useOfflineVocab makes tiktoken load BPE ranks from the embedded offline loader
// instead of fetching them over the network — deterministic, no I/O at runtime.
func useOfflineVocab() {
	offlineOnce.Do(func() { tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader()) })
}

type tiktokenCounter struct {
	enc *tiktoken.Tiktoken
}

// newTiktoken returns a tiktoken-backed counter for the given encoding name
// (e.g. "cl100k_base"), or an error if the encoding can't be loaded.
func newTiktoken(encoding string) (*tiktokenCounter, error) {
	useOfflineVocab()
	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, err
	}
	return &tiktokenCounter{enc: enc}, nil
}

func (t *tiktokenCounter) CountText(text string) int {
	return len(t.enc.Encode(text, nil, nil))
}

func (t *tiktokenCounter) Backend() Backend { return BackendTiktoken }
```

- [ ] **Step 6: Write the registry test**

Create `internal/tokenizer/registry_test.go`:
```go
package tokenizer

import "testing"

func TestGetTokenizerFallsBackToEstimator(t *testing.T) {
	// An unknown model must still return a working tokenizer (estimator).
	tok := GetTokenizer("some-unknown-model")
	if tok == nil {
		t.Fatal("GetTokenizer returned nil")
	}
	if tok.CountText("hello world this is a test") < 1 {
		t.Fatal("tokenizer counted < 1")
	}
}

func TestGetTokenizerOpenAIUsesTiktoken(t *testing.T) {
	tok := GetTokenizer("gpt-4o")
	if tok.Backend() != BackendTiktoken {
		t.Skip("offline tiktoken vocab unavailable in this environment")
	}
	if tok.CountText("hello") < 1 {
		t.Fatal("tiktoken counted < 1")
	}
}
```

- [ ] **Step 7: Run it to verify it fails**

Run: `go test ./internal/tokenizer/ -run GetTokenizer`
Expected: FAIL — `undefined: GetTokenizer`.

- [ ] **Step 8: Implement the registry**

Create `internal/tokenizer/registry.go`:
```go
package tokenizer

import "strings"

// GetTokenizer returns the best available tokenizer for a model. Resolution:
// OpenAI families -> tiktoken (cl100k_base/o200k_base), with estimator fallback
// if the offline vocab can't load; everything else -> the estimator (which
// covers Claude well). The HF backend is a follow-up.
func GetTokenizer(model string) Tokenizer {
	m := strings.ToLower(model)
	if enc := openAIEncoding(m); enc != "" {
		if t, err := newTiktoken(enc); err == nil {
			return t
		}
	}
	return EstimatingCounter{CharsPerToken: defaultCharsPerToken}
}

func openAIEncoding(model string) string {
	switch {
	case strings.HasPrefix(model, "gpt-4o"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "o200k_base"
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3.5"):
		return "cl100k_base"
	default:
		return ""
	}
}
```

- [ ] **Step 9: Run all tokenizer tests**

Run: `go test ./internal/tokenizer/`
Expected: PASS (the OpenAI test skips if offline vocab is unavailable).

- [ ] **Step 10: Commit**

```bash
git add internal/tokenizer/ go.mod go.sum
git commit -m "feat(tokenizer): rune estimator + offline tiktoken + registry"
```

---

## Task 5: detect package (7-type content detector)

**Files:**
- Create: `internal/detect/detect.go`
- Test: `internal/detect/detect_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/detect/detect_test.go`:
```go
package detect

import (
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestDetectContentType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want transform.ContentType
	}{
		{"json array", `[{"a":1},{"a":2}]`, transform.JsonArray},
		{"git diff", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", transform.GitDiff},
		{"unified diff no header", "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n line\n", transform.GitDiff},
		{"html", "<!DOCTYPE html>\n<html><body>hi</body></html>", transform.Html},
		{"search results", "src/main.go:42: func main() {\nsrc/util.go:7: var x = 1\n", transform.SearchResults},
		{"build output", "main.go:10:2: undefined: foo\nFAILED build with 1 error\n", transform.BuildOutput},
		{"source code", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n", transform.SourceCode},
		{"plain text", "the quick brown fox jumps over the lazy dog and keeps going", transform.PlainText},
		{"empty is text", "", transform.PlainText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectContentType(c.in)
			if got.Type != c.want {
				t.Errorf("DetectContentType(%q).Type = %v, want %v", c.name, got.Type, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/detect/`
Expected: FAIL — `undefined: DetectContentType`.

- [ ] **Step 3: Implement the detector**

Create `internal/detect/detect.go`:
```go
// Package detect classifies a content block into one of seven ContentType
// variants using cheap ordered heuristics. Order matters: the first matching
// rule wins. This is the legacy regex detector; the Magika ML tier is a
// follow-up.
package detect

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DetectionResult is the detector's verdict plus a rough confidence in [0,1].
type DetectionResult struct {
	Type       transform.ContentType
	Confidence float32
}

var (
	diffRe   = regexp.MustCompile(`(?m)^(diff --git |--- |\+\+\+ |@@ .* @@)`)
	htmlRe   = regexp.MustCompile(`(?i)^\s*<(!doctype|html|head|body|div|span|table|ul|ol|p|a)\b`)
	searchRe = regexp.MustCompile(`(?m)^[^\s:][^:\n]*:\d+:`)            // path:line:
	buildRe  = regexp.MustCompile(`(?im)(:\d+:\d+:|error:|warning:|FAILED|panic:|undefined:)`)
	codeRe   = regexp.MustCompile(`(?m)^\s*(package |import |func |class |def |fn |public |private |const |let |var |#include)\b`)
)

// DetectContentType returns the best-guess ContentType for content.
func DetectContentType(content string) DetectionResult {
	s := strings.TrimSpace(content)
	if s == "" {
		return DetectionResult{Type: transform.PlainText, Confidence: 1}
	}
	// 1. JSON array (parse-validated to avoid false positives).
	if strings.HasPrefix(s, "[") && looksLikeJSONArray(s) {
		return DetectionResult{Type: transform.JsonArray, Confidence: 0.95}
	}
	// 2. Diff (git or unified).
	if diffRe.MatchString(s) {
		return DetectionResult{Type: transform.GitDiff, Confidence: 0.9}
	}
	// 3. HTML.
	if htmlRe.MatchString(s) {
		return DetectionResult{Type: transform.Html, Confidence: 0.85}
	}
	// 4. Search results (grep-style path:line:).
	if searchRe.MatchString(s) {
		return DetectionResult{Type: transform.SearchResults, Confidence: 0.8}
	}
	// 5. Build output (compiler diagnostics / failures).
	if buildRe.MatchString(s) {
		return DetectionResult{Type: transform.BuildOutput, Confidence: 0.75}
	}
	// 6. Source code (language keyword at line start).
	if codeRe.MatchString(s) {
		return DetectionResult{Type: transform.SourceCode, Confidence: 0.7}
	}
	// 7. Fallback.
	return DetectionResult{Type: transform.PlainText, Confidence: 0.5}
}

func looksLikeJSONArray(s string) bool {
	var v []json.RawMessage
	return json.Unmarshal([]byte(s), &v) == nil
}
```

> Note: precedence is deliberate — diff is checked before search/build because a diff body can contain `path:line:`-like lines, and JSON-array is checked first because it is parse-validated. Cross-check exact boundaries against the research spec's `detection` subsystem during review; adjust regexes (not the ordering contract) if a fixture disagrees.

- [ ] **Step 4: Run the detector test**

Run: `go test ./internal/detect/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detect/
git commit -m "feat(detect): 7-type heuristic content detector"
```

---

## Task 6: pipeline package (orchestrator + config)

**Files:**
- Create: `internal/pipeline/config.go`, `internal/pipeline/pipeline.toml`, `internal/pipeline/pipeline.go`
- Test: `internal/pipeline/pipeline_test.go`

- [ ] **Step 1: Create the embedded default config**

Create `internal/pipeline/pipeline.toml`:
```toml
# Default compression pipeline gating thresholds. Ported from upstream defaults.
reformat_target_ratio = 0.5    # stop running reformats once current/original <= this
bloat_threshold       = 0.5    # an offload runs if its bloat estimate >= this
offload_fallback_ratio = 0.85  # ...or if reformats barely helped (ratio > this) and bloat > 0
```

Create `internal/pipeline/config.go`:
```go
package pipeline

import (
	_ "embed"

	"github.com/BurntSushi/toml"
)

//go:embed pipeline.toml
var defaultConfigTOML string

// Config holds the orchestrator's gating thresholds.
type Config struct {
	ReformatTargetRatio  float64 `toml:"reformat_target_ratio"`
	BloatThreshold       float64 `toml:"bloat_threshold"`
	OffloadFallbackRatio float64 `toml:"offload_fallback_ratio"`
}

// DefaultConfig parses the embedded pipeline.toml.
func DefaultConfig() Config {
	var c Config
	if _, err := toml.Decode(defaultConfigTOML, &c); err != nil {
		// embedded constant is known-good; fall back to literals if it ever isn't.
		return Config{ReformatTargetRatio: 0.5, BloatThreshold: 0.5, OffloadFallbackRatio: 0.85}
	}
	return c
}
```
Add dependency:
```bash
go get github.com/BurntSushi/toml
```

- [ ] **Step 2: Write the failing orchestrator test**

Create `internal/pipeline/pipeline_test.go`:
```go
package pipeline

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// --- test doubles ---

type fakeReformat struct {
	name   string
	types  []transform.ContentType
	out    string
	saved  int
}

func (f fakeReformat) Name() string                          { return f.name }
func (f fakeReformat) AppliesTo() []transform.ContentType    { return f.types }
func (f fakeReformat) Apply(content string) (transform.ReformatOutput, error) {
	return transform.ReformatOutput{Output: f.out, BytesSaved: f.saved}, nil
}

type fakeOffload struct {
	name  string
	types []transform.ContentType
	bloat float32
	out   string
	saved int
	key   string
}

func (f fakeOffload) Name() string                       { return f.name }
func (f fakeOffload) AppliesTo() []transform.ContentType { return f.types }
func (f fakeOffload) EstimateBloat(string) float32       { return f.bloat }
func (f fakeOffload) Confidence() float32                { return 1 }
func (f fakeOffload) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	store.Put(f.key, content)
	return transform.OffloadOutput{Output: f.out, BytesSaved: f.saved, CacheKey: f.key}, nil
}

func newStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPassthroughWhenNoTransforms(t *testing.T) {
	p := NewBuilder().WithConfig(DefaultConfig()).Build()
	in := "untouched content"
	r := p.Run(in, transform.PlainText, transform.CompressionContext{}, newStore(t))
	if r.Output != in {
		t.Fatalf("Output = %q, want passthrough %q", r.Output, in)
	}
	if r.BytesSaved != 0 || len(r.StepsApplied) != 0 || len(r.CacheKeys) != 0 {
		t.Fatalf("expected empty result, got %+v", r)
	}
}

func TestReformatRunsAndSkipsZeroSaved(t *testing.T) {
	good := fakeReformat{name: "good", types: []transform.ContentType{transform.JsonArray}, out: "small", saved: 10}
	noop := fakeReformat{name: "noop", types: []transform.ContentType{transform.JsonArray}, out: "small", saved: 0}
	p := NewBuilder().WithReformat(good).WithReformat(noop).Build()
	r := p.Run("biiiig input", transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if r.Output != "small" || r.BytesSaved != 10 {
		t.Fatalf("got %+v", r)
	}
	if len(r.StepsApplied) != 1 || r.StepsApplied[0] != "good" {
		t.Fatalf("StepsApplied = %v, want [good] (noop skipped)", r.StepsApplied)
	}
}

func TestReformatEarlyStopAtTargetRatio(t *testing.T) {
	// First reformat brings 100 -> 40 (ratio 0.4 <= 0.5) so the second must not run.
	first := fakeReformat{name: "first", types: []transform.ContentType{transform.PlainText}, out: strings.Repeat("x", 40), saved: 60}
	second := fakeReformat{name: "second", types: []transform.ContentType{transform.PlainText}, out: "should-not-appear", saved: 10}
	p := NewBuilder().WithReformat(first).WithReformat(second).Build()
	r := p.Run(strings.Repeat("x", 100), transform.PlainText, transform.CompressionContext{}, newStore(t))
	if len(r.StepsApplied) != 1 || r.StepsApplied[0] != "first" {
		t.Fatalf("StepsApplied = %v, want [first] (early-stop)", r.StepsApplied)
	}
}

func TestOffloadGatedByBloatThreshold(t *testing.T) {
	hi := fakeOffload{name: "hi", types: []transform.ContentType{transform.JsonArray}, bloat: 0.9, out: "off", saved: 5, key: "k1"}
	lo := fakeOffload{name: "lo", types: []transform.ContentType{transform.JsonArray}, bloat: 0.1, out: "off2", saved: 5, key: "k2"}
	p := NewBuilder().WithOffload(hi).WithOffload(lo).Build()
	r := p.Run("input-with-no-reformat", transform.JsonArray, transform.CompressionContext{}, newStore(t))
	// reformatRatio == 1.0 > 0.85 fallback, so lo (bloat 0.1 > 0) ALSO runs.
	if len(r.CacheKeys) != 2 {
		t.Fatalf("CacheKeys = %v, want both offloads via fallback path", r.CacheKeys)
	}
}

func TestOffloadFallbackNotTriggeredWhenReformatsHelped(t *testing.T) {
	// Reformat cuts ratio to 0.3 (<=0.5 early-stop, and <0.85 so no fallback).
	rf := fakeReformat{name: "rf", types: []transform.ContentType{transform.JsonArray}, out: strings.Repeat("y", 30), saved: 70}
	lo := fakeOffload{name: "lo", types: []transform.ContentType{transform.JsonArray}, bloat: 0.2, out: "off", saved: 5, key: "k"}
	p := NewBuilder().WithReformat(rf).WithOffload(lo).Build()
	r := p.Run(strings.Repeat("y", 100), transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if len(r.CacheKeys) != 0 {
		t.Fatalf("low-bloat offload should be skipped when reformats helped; CacheKeys=%v", r.CacheKeys)
	}
}

func TestCacheKeysOnlyFromOffloads(t *testing.T) {
	rf := fakeReformat{name: "rf", types: []transform.ContentType{transform.JsonArray}, out: "tiny", saved: 5}
	off := fakeOffload{name: "off", types: []transform.ContentType{transform.JsonArray}, bloat: 0.9, out: "t", saved: 1, key: "kk"}
	p := NewBuilder().WithReformat(rf).WithOffload(off).Build()
	r := p.Run("xxxxxxxxxx", transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if len(r.CacheKeys) != 1 || r.CacheKeys[0] != "kk" {
		t.Fatalf("CacheKeys = %v, want [kk] (reformats never add keys)", r.CacheKeys)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/pipeline/`
Expected: FAIL — `undefined: NewBuilder`.

- [ ] **Step 4: Implement the orchestrator**

Create `internal/pipeline/pipeline.go`:
```go
package pipeline

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Result is the outcome of a pipeline run.
type Result struct {
	Output       string
	BytesSaved   int
	StepsApplied []string
	CacheKeys    []string // from accepted OFFLOADS only; reformats never add keys
}

// Pipeline orchestrates reformats (lossless) then offloads (info-preserving),
// dispatched by ContentType. Sequential by design for v0.
type Pipeline struct {
	reformatsByType map[transform.ContentType][]transform.ReformatTransform
	offloadsByType  map[transform.ContentType][]transform.OffloadTransform
	config          Config
}

// Builder assembles a Pipeline, registering each transform under every type in
// its AppliesTo() list.
type Builder struct {
	reformats []transform.ReformatTransform
	offloads  []transform.OffloadTransform
	config    Config
	hasConfig bool
}

func NewBuilder() *Builder { return &Builder{} }

func (b *Builder) WithReformat(t transform.ReformatTransform) *Builder {
	b.reformats = append(b.reformats, t)
	return b
}
func (b *Builder) WithOffload(t transform.OffloadTransform) *Builder {
	b.offloads = append(b.offloads, t)
	return b
}
func (b *Builder) WithConfig(c Config) *Builder {
	b.config, b.hasConfig = c, true
	return b
}

func (b *Builder) Build() *Pipeline {
	p := &Pipeline{
		reformatsByType: map[transform.ContentType][]transform.ReformatTransform{},
		offloadsByType:  map[transform.ContentType][]transform.OffloadTransform{},
		config:          b.config,
	}
	if !b.hasConfig {
		p.config = DefaultConfig()
	}
	for _, t := range b.reformats {
		for _, ct := range t.AppliesTo() {
			p.reformatsByType[ct] = append(p.reformatsByType[ct], t)
		}
	}
	for _, t := range b.offloads {
		for _, ct := range t.AppliesTo() {
			p.offloadsByType[ct] = append(p.offloadsByType[ct], t)
		}
	}
	return p
}

// Run compresses content of the given type. It always returns some output (the
// input verbatim if every stage skips). Errors from transforms are swallowed
// (skip-and-continue); they never propagate or panic.
func (p *Pipeline) Run(content string, ct transform.ContentType, ctx transform.CompressionContext, store ccr.Store) Result {
	originalLen := len(content)
	current := content
	var steps []string
	bytesSaved := 0

	// Phase 1: reformats, sequential, registration order, early-stop.
	for _, rf := range p.reformatsByType[ct] {
		if originalLen > 0 && float64(len(current))/float64(originalLen) <= p.config.ReformatTargetRatio {
			break // already small enough
		}
		out, err := rf.Apply(current)
		if err != nil || out.BytesSaved == 0 {
			continue
		}
		current = out.Output
		steps = append(steps, rf.Name())
		bytesSaved = saturatingAdd(bytesSaved, out.BytesSaved)
	}

	// Phase 2: offloads, gated.
	reformatRatio := 1.0
	if originalLen > 0 {
		reformatRatio = float64(len(current)) / float64(originalLen)
	}
	var cacheKeys []string
	for _, off := range p.offloadsByType[ct] {
		score := off.EstimateBloat(current)
		run := float64(score) >= p.config.BloatThreshold ||
			(reformatRatio > p.config.OffloadFallbackRatio && score > 0)
		if !run {
			continue
		}
		out, err := off.Apply(current, ctx, store)
		if err != nil || out.BytesSaved == 0 {
			continue
		}
		current = out.Output
		steps = append(steps, off.Name())
		bytesSaved = saturatingAdd(bytesSaved, out.BytesSaved)
		cacheKeys = append(cacheKeys, out.CacheKey)
	}

	return Result{Output: current, BytesSaved: bytesSaved, StepsApplied: steps, CacheKeys: cacheKeys}
}

func saturatingAdd(a, b int) int {
	s := a + b
	if s < a {
		return int(^uint(0) >> 1) // max int
	}
	return s
}
```

- [ ] **Step 5: Run the pipeline tests**

Run: `go test ./internal/pipeline/`
Expected: PASS (all six).

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/ go.mod go.sum
git commit -m "feat(pipeline): orchestrator with exact reformat/offload gating + embedded config"
```

---

## Task 7: router package + foundation integration

**Files:**
- Create: `internal/router/router.go`
- Test: `internal/router/router_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/router/router_test.go`:
```go
package router

import (
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func newStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRouterDetect(t *testing.T) {
	r := New(pipeline.NewBuilder().Build())
	if got := r.Detect(`[{"a":1}]`); got.Type != transform.JsonArray {
		t.Fatalf("Detect json = %v", got.Type)
	}
}

func TestRouterCompressPassthrough(t *testing.T) {
	r := New(pipeline.NewBuilder().Build())
	in := "plain text body that should pass through untouched"
	res := r.Compress(in, transform.CompressionContext{}, newStore(t))
	if res.Output != in {
		t.Fatalf("passthrough failed: %q", res.Output)
	}
}

func TestRouterCompressDeterministic(t *testing.T) {
	// I4: same input twice -> byte-equal output.
	r := New(pipeline.NewBuilder().Build())
	in := `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`
	a := r.Compress(in, transform.CompressionContext{}, newStore(t)).Output
	b := r.Compress(in, transform.CompressionContext{}, newStore(t)).Output
	if a != b {
		t.Fatalf("non-deterministic: %q != %q", a, b)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/router/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement the router**

Create `internal/router/router.go`:
```go
// Package router detects a content block's type and runs it through the
// compression pipeline. It is the single seam the entrypoints (MCP/proxy/CLI)
// call to compress one piece of content.
package router

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/detect"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Router pairs the content detector with a compression pipeline.
type Router struct {
	pipeline *pipeline.Pipeline
}

// New builds a Router over the given pipeline.
func New(p *pipeline.Pipeline) *Router { return &Router{pipeline: p} }

// Detect classifies content.
func (r *Router) Detect(content string) detect.DetectionResult {
	return detect.DetectContentType(content)
}

// Compress detects the content type and runs the pipeline. With no registered
// transforms it returns the input verbatim (faithful passthrough).
func (r *Router) Compress(content string, ctx transform.CompressionContext, store ccr.Store) pipeline.Result {
	d := detect.DetectContentType(content)
	return r.pipeline.Run(content, d.Type, ctx, store)
}
```

- [ ] **Step 4: Run the router tests**

Run: `go test ./internal/router/`
Expected: PASS.

- [ ] **Step 5: Full build + vet + test sweep**

Run:
```bash
go build ./... && go vet ./... && go test ./...
```
Expected: all packages PASS, no vet warnings.

- [ ] **Step 6: Commit + update GOALS**

Edit `GOALS.md`, check the Foundation box:
```markdown
- [x] (Plan 1) Foundation: transform interfaces, CCR, tokenizer, detector, pipeline, router.
```
```bash
git add internal/router/ GOALS.md
git commit -m "feat(router): detect+compress seam; foundation complete (passthrough spine)"
```

- [ ] **Step 7: Mark beads tasks done**

```bash
bd update <foundation-task-ids> --status done
bd list
```
Expected: foundation tasks show done.

---

## Self-Review (completed during planning)

- **Spec coverage:** Foundation covers spec §5 interfaces (transform, pipeline.Result, Router, ccr.Store, tokenizer.Tokenizer), §6 rows for pipeline/detect/ccr/tokenizer/router, §8 v0.1 items 1–4, 7, 8, 9 (BM25/signals deferred to Plan 2), and §11 determinism (I4) + never-inflate. The token-reject (I5) and byte-surgery (I1) live in `livezone` (v0.2, Plan in next cycle) — correctly out of this plan. SmartCrusher, heuristic compressors, MCP, config/paths → Plans 2–4.
- **Placeholder scan:** every code step contains complete, compilable Go. No TBD/TODO/"handle errors".
- **Type consistency:** `transform.ContentType`/`String()`, `ccr.Store{Put,Get,Len}`, `ccr.FromConfig`/`BackendConfig{Kind,Capacity,TTLSeconds,Path}`, `ccr.ComputeKey`/`MarkerFor`/`ParseMarker`/`MarkerForCell`/`MarkerForLossy`, `tokenizer.Tokenizer{CountText,Backend}`/`GetTokenizer`/`EstimatingCounter`, `pipeline.Config{ReformatTargetRatio,BloatThreshold,OffloadFallbackRatio}`/`NewBuilder`/`WithReformat`/`WithOffload`/`WithConfig`/`Build`/`Run`/`Result`, `detect.DetectContentType`/`DetectionResult{Type,Confidence}`, `router.New`/`Detect`/`Compress` — names are consistent across all tasks and match spec §5.

---

## Dependency note for executor

Tasks 2 and 3 are mutually referenced (`transform` imports `ccr`). Implement **Task 3 (ccr) before re-running Task 2's test**. The recommended execution order is: Task 1 → Task 3 → Task 2 → Task 4 → Task 5 → Task 6 → Task 7. (Task 0 beads setup first.)
```

