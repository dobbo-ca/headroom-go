# Semantic Response Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an opt-in semantic response cache that returns a stored provider response when a new request embeds close to one already seen.

**Architecture:** Two new packages. `internal/embed` turns text into a `[]float32` with one HTTP POST to a local Ollama endpoint. `internal/semcache` normalizes a request, scans a bounded in-memory vector index for the nearest neighbour, and reads or writes the response payload through the existing `ccr.Store`. Nothing wires into a gateway in this plan — the v0.2 `livezone` gateway does not exist yet, so both packages ship standalone and fully tested.

**Tech Stack:** Go 1.25, stdlib `net/http` and `net/http/httptest`, existing `internal/ccr`. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-08-19-semantic-response-cache-design.md`

## Global Constraints

- **`CGO_ENABLED=0` must hold.** No new cgo dependency, direct or transitive. The local model is reached over HTTP; it is never linked in.
- **No new entries in `go.mod`.** Everything here is stdlib plus `internal/ccr`.
- **The cache is off by default.** `Config.Enabled` zero value is `false`.
- **A missing or failing model is a miss, never an error.** Connection refused, timeout, malformed response: return "no hit" and let the caller proceed.
- **Never serve a request carrying tool results.** Skip the lookup entirely.
- **The model tag is part of the cache key.** Vectors from one model tag must never be compared against another.
- **Package layout:** `internal/<pkg>`, one concern per package, one file per concern (project `CLAUDE.md`).
- **Defaults, copied verbatim from spec section 5:** endpoint `http://localhost:11434`, model `nomic-embed-text`, threshold `0.97`, timeout `2s`.

---

### Task 1: `internal/embed` — HTTP embedding client

Turns text into a vector by calling Ollama's `/api/embeddings`. Knows nothing about caching.

**Files:**
- Create: `internal/embed/embed.go`
- Test: `internal/embed/embed_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Client struct { Endpoint string; Model string; Timeout time.Duration; HTTP *http.Client }`
  - `func New(endpoint, model string, timeout time.Duration) *Client`
  - `func (c *Client) Embed(ctx context.Context, text string) ([]float32, error)`
  - `const DefaultEndpoint = "http://localhost:11434"`
  - `const DefaultModel = "nomic-embed-text"`
  - `const DefaultTimeout = 2 * time.Second`

- [ ] **Step 1: Write the failing tests**

Create `internal/embed/embed_test.go`:

```go
package embed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEmbedReturnsVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path = %q, want /api/embeddings", r.URL.Path)
		}
		w.Write([]byte(`{"embedding":[0.1,0.2,0.3]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	v, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(v) != 3 {
		t.Fatalf("len(v) = %d, want 3", len(v))
	}
	if v[0] != 0.1 {
		t.Errorf("v[0] = %v, want 0.1", v[0])
	}
}

func TestEmbedSendsModelAndPrompt(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// io.ReadAll, not Body.Read: a single Read may return a short buffer.
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		got = string(b)
		w.Write([]byte(`{"embedding":[1]}`))
	}))
	defer srv.Close()

	New(srv.URL, "my-model", time.Second).Embed(context.Background(), "some text")
	if !strings.Contains(got, `"model":"my-model"`) {
		t.Errorf("body %q missing model", got)
	}
	if !strings.Contains(got, `"prompt":"some text"`) {
		t.Errorf("body %q missing prompt", got)
	}
}

func TestEmbedErrorsWhenEndpointDown(t *testing.T) {
	// Port 1 is reserved and never listening.
	c := New("http://127.0.0.1:1", "test-model", 200*time.Millisecond)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error with no server")
	}
}

func TestEmbedErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error on HTTP 500")
	}
}

func TestEmbedErrorsOnEmptyVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"embedding":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error on empty embedding")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/embed/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/embed/embed.go`:

```go
// Package embed turns text into a vector by calling a local embedding model
// over HTTP. The model runs in its own process (Ollama), so the core stays
// CGO_ENABLED=0 and cross-compiles.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Defaults, per the design spec. All three are overridable config.
const (
	DefaultEndpoint = "http://localhost:11434"
	DefaultModel    = "nomic-embed-text"
	DefaultTimeout  = 2 * time.Second
)

// Client calls an Ollama-compatible /api/embeddings endpoint.
type Client struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
	HTTP     *http.Client
}

// New builds a Client. Empty endpoint, empty model, or non-positive timeout
// fall back to the package defaults.
func New(endpoint, model string, timeout time.Duration) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if model == "" {
		model = DefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		Endpoint: endpoint,
		Model:    model,
		Timeout:  timeout,
		HTTP:     &http.Client{Timeout: timeout},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns the vector for text. Every failure path returns an error; the
// caller treats an error as a cache miss, never as a fatal condition.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.Model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d", resp.StatusCode)
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty embedding")
	}
	return out.Embedding, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/embed/ -v`
Expected: PASS, all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/embed/
git commit -m "feat(embed): HTTP client for a local embedding model

Reaches Ollama's /api/embeddings over localhost so no cgo enters the
binary. Every failure path returns an error; callers treat that as a miss.

Refs: hr-47g.25"
```

---

### Task 2: `internal/semcache` — normalization and cosine similarity

The two pure functions the cache is built from. No HTTP, no storage, no state.

**Files:**
- Create: `internal/semcache/normalize.go`
- Create: `internal/semcache/cosine.go`
- Test: `internal/semcache/normalize_test.go`
- Test: `internal/semcache/cosine_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `func Normalize(s string) string`
  - `func Cosine(a, b []float32) float32`

- [ ] **Step 1: Write the failing normalization test**

Create `internal/semcache/normalize_test.go`:

```go
package semcache

import "testing"

func TestNormalizeCollapsesWhitespace(t *testing.T) {
	if got, want := Normalize("read   the\t\tfile"), "read the file"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeTrimsEnds(t *testing.T) {
	if got, want := Normalize("  hello  "), "hello"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeCollapsesNewlines(t *testing.T) {
	if got, want := Normalize("a\n\n\nb"), "a b"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeLowercases(t *testing.T) {
	if got, want := Normalize("Read The File"), "read the file"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	once := Normalize("  Mixed   Case\n\ttext ")
	if twice := Normalize(once); once != twice {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}

func TestNormalizeEmptyStaysEmpty(t *testing.T) {
	if got := Normalize("   \n\t "); got != "" {
		t.Errorf("Normalize = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/semcache/ -run Normalize`
Expected: FAIL — `undefined: Normalize`.

- [ ] **Step 3: Write the normalization implementation**

Create `internal/semcache/normalize.go`:

```go
// Package semcache returns a stored provider response when a new request
// embeds close to one already seen. It is opt-in and lossy by design: a hit
// answers a question with the answer to a similar-but-different question.
package semcache

import "strings"

// Normalize reduces a request to the text that gets embedded. Two requests
// that differ only in whitespace or letter case must produce one string, so
// that trivial formatting differences do not defeat a cache hit.
//
// This never touches the bytes sent upstream. On a miss the original request
// is forwarded verbatim, so the byte-surgery invariant (I1) is untouched.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/semcache/ -run Normalize -v`
Expected: PASS, all six tests.

- [ ] **Step 5: Write the failing cosine test**

Create `internal/semcache/cosine_test.go`:

```go
package semcache

import "testing"

func nearly(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

func TestCosineIdenticalVectorsIsOne(t *testing.T) {
	v := []float32{1, 2, 3}
	if got := Cosine(v, v); !nearly(got, 1) {
		t.Errorf("Cosine = %v, want 1", got)
	}
}

func TestCosineOrthogonalIsZero(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); !nearly(got, 0) {
		t.Errorf("Cosine = %v, want 0", got)
	}
}

func TestCosineOppositeIsMinusOne(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{-1, 0}); !nearly(got, -1) {
		t.Errorf("Cosine = %v, want -1", got)
	}
}

func TestCosineIgnoresMagnitude(t *testing.T) {
	if got := Cosine([]float32{1, 2}, []float32{10, 20}); !nearly(got, 1) {
		t.Errorf("Cosine = %v, want 1", got)
	}
}

func TestCosineLengthMismatchIsZero(t *testing.T) {
	if got := Cosine([]float32{1, 2, 3}, []float32{1, 2}); got != 0 {
		t.Errorf("Cosine = %v, want 0 for mismatched lengths", got)
	}
}

func TestCosineZeroVectorIsZero(t *testing.T) {
	if got := Cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("Cosine = %v, want 0 for a zero vector", got)
	}
}

func TestCosineEmptyIsZero(t *testing.T) {
	if got := Cosine(nil, nil); got != 0 {
		t.Errorf("Cosine = %v, want 0 for empty vectors", got)
	}
}
```

- [ ] **Step 6: Run it to verify it fails**

Run: `go test ./internal/semcache/ -run Cosine`
Expected: FAIL — `undefined: Cosine`.

- [ ] **Step 7: Write the cosine implementation**

Create `internal/semcache/cosine.go`:

```go
package semcache

import "math"

// Cosine returns the cosine similarity of a and b, in [-1, 1].
//
// It returns 0 for mismatched lengths, empty input, or a zero-magnitude
// vector. Those are all "no useful comparison", and 0 is far below any usable
// threshold, so a caller that ignores the distinction still behaves correctly.
//
// Accumulate in float64: a float32 sum over a 768-dimension vector loses
// enough precision to move a score across a 0.97 threshold.
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
```

- [ ] **Step 8: Run all package tests**

Run: `go test ./internal/semcache/ -v`
Expected: PASS, all thirteen tests.

- [ ] **Step 9: Commit**

```bash
git add internal/semcache/
git commit -m "feat(semcache): request normalization and cosine similarity

Normalize collapses whitespace and case so formatting differences do not
defeat a hit. Cosine accumulates in float64 because a float32 sum over a
768-dimension vector can move a score across the 0.97 threshold.

Refs: hr-47g.25"
```

---

### Task 3: `internal/semcache` — the cache itself

Ties the pieces together: skip rules, bounded vector index, CCR payload storage, threshold decision.

**Files:**
- Create: `internal/semcache/cache.go`
- Test: `internal/semcache/cache_test.go`

**Interfaces:**
- Consumes:
  - `embed.Client` and `embed.Embed` from Task 1.
  - `Normalize` and `Cosine` from Task 2.
  - `ccr.Store` (`Put(hash, payload string)`, `Get(hash string) (string, bool)`) and `ccr.ComputeKey(payload []byte) string` — both already exist.
- Produces:
  - `type Embedder interface { Embed(ctx context.Context, text string) ([]float32, error) }`
  - `type Config struct { Enabled bool; Threshold float32; MaxEntries int; Model string }`
  - `func DefaultConfig() Config`
  - `type Cache struct { ... }`
  - `func New(cfg Config, e Embedder, store ccr.Store) *Cache`
  - `type Request struct { Text string; HasToolResults bool }`
  - `func (c *Cache) Lookup(ctx context.Context, req Request) (string, bool)`
  - `func (c *Cache) Store(ctx context.Context, req Request, response string)`
  - `const DefaultThreshold = 0.97`
  - `const DefaultMaxEntries = 2000`

- [ ] **Step 1: Write the failing tests**

Create `internal/semcache/cache_test.go`:

```go
package semcache

import (
	"context"
	"errors"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// fakeStore is a minimal in-memory ccr.Store. The real backends need a blank
// import and a TTL; this keeps the cache tests about the cache.
type fakeStore struct{ m map[string]string }

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]string{}} }

func (f *fakeStore) Put(hash, payload string) { f.m[hash] = payload }
func (f *fakeStore) Get(hash string) (string, bool) {
	v, ok := f.m[hash]
	return v, ok
}
func (f *fakeStore) Len() int { return len(f.m) }

var _ ccr.Store = (*fakeStore)(nil)

// fixedEmbedder returns a preset vector per text, so tests control similarity
// exactly instead of depending on a real model.
type fixedEmbedder struct {
	vecs  map[string][]float32
	calls int
	err   error
}

func (f *fixedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.vecs[text]
	if !ok {
		return nil, errors.New("no vector for text")
	}
	return v, nil
}

func enabledConfig() Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	return cfg
}

func TestDefaultConfigIsDisabled(t *testing.T) {
	if DefaultConfig().Enabled {
		t.Fatal("DefaultConfig is enabled; the cache must be off by default")
	}
}

func TestDisabledCacheNeverEmbeds(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{"hello": {1, 0}}}
	c := New(DefaultConfig(), e, newFakeStore())

	if _, ok := c.Lookup(context.Background(), Request{Text: "hello"}); ok {
		t.Error("disabled cache returned a hit")
	}
	c.Store(context.Background(), Request{Text: "hello"}, "response")
	if e.calls != 0 {
		t.Errorf("embedder called %d times while disabled, want 0", e.calls)
	}
}

func TestStoreThenLookupIdenticalIsHit(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{"hello": {1, 0}}}
	c := New(enabledConfig(), e, newFakeStore())

	c.Store(context.Background(), Request{Text: "hello"}, "the response")
	got, ok := c.Lookup(context.Background(), Request{Text: "hello"})
	if !ok {
		t.Fatal("identical request missed")
	}
	if got != "the response" {
		t.Errorf("got %q, want %q", got, "the response")
	}
}

func TestLookupBelowThresholdIsMiss(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{
		"hello": {1, 0},
		"other": {0, 1}, // orthogonal, cosine 0
	}}
	c := New(enabledConfig(), e, newFakeStore())

	c.Store(context.Background(), Request{Text: "hello"}, "the response")
	if _, ok := c.Lookup(context.Background(), Request{Text: "other"}); ok {
		t.Error("orthogonal request hit")
	}
}

func TestLookupNormalizesBeforeEmbedding(t *testing.T) {
	// Only the normalized form has a vector, so a hit proves normalization ran.
	e := &fixedEmbedder{vecs: map[string][]float32{"read the file": {1, 0}}}
	c := New(enabledConfig(), e, newFakeStore())

	c.Store(context.Background(), Request{Text: "Read  the\tfile"}, "the response")
	if _, ok := c.Lookup(context.Background(), Request{Text: "  READ THE FILE "}); !ok {
		t.Error("normalized-equal requests missed")
	}
}

func TestToolResultsAreNeverCached(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{"hello": {1, 0}}}
	c := New(enabledConfig(), e, newFakeStore())

	c.Store(context.Background(), Request{Text: "hello", HasToolResults: true}, "the response")
	if e.calls != 0 {
		t.Errorf("embedder called %d times storing tool results, want 0", e.calls)
	}
	if _, ok := c.Lookup(context.Background(), Request{Text: "hello", HasToolResults: true}); ok {
		t.Error("request with tool results returned a hit")
	}
	if e.calls != 0 {
		t.Errorf("embedder called %d times looking up tool results, want 0", e.calls)
	}
}

func TestEmbedderFailureIsMissNotError(t *testing.T) {
	e := &fixedEmbedder{err: errors.New("connection refused")}
	c := New(enabledConfig(), e, newFakeStore())

	// Must not panic, must not hit.
	c.Store(context.Background(), Request{Text: "hello"}, "the response")
	if _, ok := c.Lookup(context.Background(), Request{Text: "hello"}); ok {
		t.Error("failing embedder returned a hit")
	}
}

func TestIndexIsBounded(t *testing.T) {
	cfg := enabledConfig()
	cfg.MaxEntries = 2
	e := &fixedEmbedder{vecs: map[string][]float32{
		"a": {1, 0}, "b": {0, 1}, "c": {1, 1},
	}}
	c := New(cfg, e, newFakeStore())

	c.Store(context.Background(), Request{Text: "a"}, "ra")
	c.Store(context.Background(), Request{Text: "b"}, "rb")
	c.Store(context.Background(), Request{Text: "c"}, "rc")

	if got := c.Len(); got != 2 {
		t.Errorf("index len = %d, want 2", got)
	}
	// "a" was evicted first (FIFO), so it must now miss.
	if _, ok := c.Lookup(context.Background(), Request{Text: "a"}); ok {
		t.Error("evicted entry still hit")
	}
	if _, ok := c.Lookup(context.Background(), Request{Text: "c"}); !ok {
		t.Error("newest entry missed")
	}
}

func TestModelTagSeparatesPayloadKeys(t *testing.T) {
	// Test payloadKey directly. Going through Lookup would pass for the wrong
	// reason: two Cache values have two indexes, so the miss proves nothing
	// about the key.
	vec := []float32{1, 0}

	cfgA := enabledConfig()
	cfgA.Model = "model-a"
	cfgB := enabledConfig()
	cfgB.Model = "model-b"

	keyA := New(cfgA, nil, newFakeStore()).payloadKey(vec)
	keyB := New(cfgB, nil, newFakeStore()).payloadKey(vec)

	if keyA == keyB {
		t.Errorf("payloadKey = %q for both model tags; the tag must separate them", keyA)
	}
}

func TestPayloadKeyIsStableForSameModelAndVector(t *testing.T) {
	cfg := enabledConfig()
	cfg.Model = "model-a"
	vec := []float32{1, 0}

	first := New(cfg, nil, newFakeStore()).payloadKey(vec)
	second := New(cfg, nil, newFakeStore()).payloadKey(vec)
	if first != second {
		t.Errorf("payloadKey not stable: %q then %q", first, second)
	}
}

func TestDifferentVectorsGetDifferentKeys(t *testing.T) {
	cfg := enabledConfig()
	cfg.Model = "model-a"
	c := New(cfg, nil, newFakeStore())

	if a, b := c.payloadKey([]float32{1, 0}), c.payloadKey([]float32{0, 1}); a == b {
		t.Errorf("payloadKey = %q for two different vectors", a)
	}
}

func TestEmptyRequestIsMiss(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{"": {1, 0}}}
	c := New(enabledConfig(), e, newFakeStore())

	c.Store(context.Background(), Request{Text: "   "}, "the response")
	if e.calls != 0 {
		t.Errorf("embedder called %d times on empty text, want 0", e.calls)
	}
	if _, ok := c.Lookup(context.Background(), Request{Text: ""}); ok {
		t.Error("empty request returned a hit")
	}
}

func TestConcurrentLookupAndStore(t *testing.T) {
	e := &fixedEmbedder{vecs: map[string][]float32{"hello": {1, 0}}}
	c := New(enabledConfig(), e, newFakeStore())

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.Store(context.Background(), Request{Text: "hello"}, "the response")
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		c.Lookup(context.Background(), Request{Text: "hello"})
	}
	<-done
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/semcache/ -run 'Cache|Config|Store|Lookup|Tool|Embedder|Index|Model|Payload|Empty|Concurrent'`
Expected: FAIL — `undefined: New`, `undefined: Config`.

- [ ] **Step 3: Write the implementation**

Create `internal/semcache/cache.go`:

```go
package semcache

import (
	"context"
	"sync"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// Defaults, per the design spec. Starting points chosen to be safe, not
// measured optima; tune Threshold against real traffic before trusting it.
const (
	DefaultThreshold  = 0.97
	DefaultMaxEntries = 2000
)

// Embedder turns text into a vector. internal/embed.Client satisfies this.
// The cache depends on the interface so tests need no HTTP server.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Config controls the cache. Enabled is false in the zero value: the cache is
// opt-in because a hit answers a question with the answer to a different one.
type Config struct {
	Enabled    bool
	Threshold  float32
	MaxEntries int
	Model      string // tag of the embedding model; part of the payload key
}

// DefaultConfig returns the documented defaults, disabled.
func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		Threshold:  DefaultThreshold,
		MaxEntries: DefaultMaxEntries,
		Model:      "",
	}
}

// Request is one candidate for caching.
type Request struct {
	Text string
	// HasToolResults marks a request carrying tool output. Such a request is
	// never cached and never served: tool output is specific to a moment and a
	// working tree, so two requests reading the same file can deserve
	// different answers.
	HasToolResults bool
}

// entry pairs a vector with the CCR key holding its response.
type entry struct {
	vec []float32
	key string
}

// Cache holds a bounded in-memory vector index and reads response payloads
// from a ccr.Store.
//
// The split exists because ccr.Store is Put/Get/Len only and cannot be
// iterated, and widening that interface would change a type the pipeline and
// every transform depend on.
//
// ponytail: linear cosine scan, O(n) per lookup, and the index is memory-only
// so a restart starts cold. Fine to a few thousand entries. Upgrade path: an
// ANN index, or persisted vectors once a pure-Go vector store exists.
type Cache struct {
	cfg   Config
	embed Embedder
	store ccr.Store

	mu    sync.RWMutex
	index []entry
}

// New builds a Cache. A non-positive Threshold or MaxEntries falls back to the
// package default, so a partly-filled Config cannot disable the safety bound.
func New(cfg Config, e Embedder, store ccr.Store) *Cache {
	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultThreshold
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	return &Cache{cfg: cfg, embed: e, store: store}
}

// Len reports how many vectors the index holds.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.index)
}

// vectorFor normalizes and embeds, or reports that this request must be
// skipped. Every skip reason collapses to ok=false: disabled, tool results,
// empty text, or an embedder that failed. A failing model is a miss, never an
// error the caller must handle.
func (c *Cache) vectorFor(ctx context.Context, req Request) ([]float32, bool) {
	if !c.cfg.Enabled || req.HasToolResults {
		return nil, false
	}
	text := Normalize(req.Text)
	if text == "" {
		return nil, false
	}
	vec, err := c.embed.Embed(ctx, text)
	if err != nil || len(vec) == 0 {
		return nil, false
	}
	return vec, true
}

// payloadKey binds a response to the model that produced its vector, so a
// model upgrade cannot serve payloads embedded in a different vector space.
func (c *Cache) payloadKey(vec []float32) string {
	h := make([]byte, 0, len(c.cfg.Model)+1+len(vec)*4)
	h = append(h, c.cfg.Model...)
	h = append(h, 0)
	for _, f := range vec {
		b := float32bits(f)
		h = append(h, byte(b), byte(b>>8), byte(b>>16), byte(b>>24))
	}
	return ccr.ComputeKey(h)
}

// Lookup returns a stored response when some indexed request scores at or
// above the threshold. The second return is false for every miss and every
// skip; the caller then proceeds exactly as if no cache existed.
func (c *Cache) Lookup(ctx context.Context, req Request) (string, bool) {
	vec, ok := c.vectorFor(ctx, req)
	if !ok {
		return "", false
	}

	c.mu.RLock()
	var bestKey string
	var best float32
	for _, e := range c.index {
		if s := Cosine(vec, e.vec); s > best {
			best, bestKey = s, e.key
		}
	}
	c.mu.RUnlock()

	if best < c.cfg.Threshold || bestKey == "" {
		return "", false
	}
	return c.store.Get(bestKey)
}

// Store records a response against the request that produced it. It is a
// no-op for anything vectorFor skips.
func (c *Cache) Store(ctx context.Context, req Request, response string) {
	vec, ok := c.vectorFor(ctx, req)
	if !ok {
		return
	}
	key := c.payloadKey(vec)
	c.store.Put(key, response)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.index = append(c.index, entry{vec: vec, key: key})
	if over := len(c.index) - c.cfg.MaxEntries; over > 0 {
		c.index = c.index[over:] // FIFO, matching ccr's in-memory backend
	}
}
```

- [ ] **Step 4: Add the float32 bit helper**

The `payloadKey` method needs a bit-pattern view of each float. Append to `internal/semcache/cosine.go`:

```go
// float32bits is math.Float32bits, named locally so cache.go does not import
// math just for one conversion.
func float32bits(f float32) uint32 { return math.Float32bits(f) }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/semcache/ -v`
Expected: PASS, all tests in the package.

- [ ] **Step 6: Run the race detector**

Run: `go test -race ./internal/semcache/`
Expected: PASS, no race reported. `TestConcurrentLookupAndStore` exercises the lock.

- [ ] **Step 7: Verify the whole tree still builds without cgo**

Run: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./...`
Expected: both succeed.

- [ ] **Step 8: Verify cross-compilation**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...`
Expected: both succeed. This is the constraint the whole HTTP-not-cgo decision exists to protect.

- [ ] **Step 9: Verify no new module dependency appeared**

Run: `go mod tidy && git diff --exit-code go.mod go.sum`
Expected: exit 0, no diff. Everything added is stdlib plus `internal/ccr`.

- [ ] **Step 10: Format and vet**

Run: `gofmt -l . && go vet ./...`
Expected: `gofmt -l` prints nothing; `go vet` reports nothing.

- [ ] **Step 11: Commit**

```bash
git add internal/semcache/
git commit -m "feat(semcache): opt-in semantic response cache

Bounded in-memory vector index over a linear cosine scan; response
payloads live in the ccr.Store and inherit its TTL. Disabled by default.

Safety rules enforced in vectorFor: never cache or serve a request that
carries tool results, and treat an embedder failure as a miss rather than
an error. The model tag is mixed into the payload key so a model upgrade
cannot serve vectors from a different embedding space.

Refs: hr-47g.25"
```

---

### Task 4: Wire the defaults together and document the seam

The two packages exist but nothing shows how they compose. This task adds the constructor that a future gateway calls, plus the package documentation an engineer reads first.

**Files:**
- Create: `internal/semcache/wire.go`
- Test: `internal/semcache/wire_test.go`
- Modify: `README.md` (add a "Semantic cache" section)

**Interfaces:**
- Consumes: `New`, `Config`, `DefaultConfig` from Task 3; `embed.New` from Task 1.
- Produces:
  - `type Options struct { Enabled bool; Endpoint string; Model string; Threshold float32; MaxEntries int; Timeout time.Duration }`
  - `func FromOptions(opts Options, store ccr.Store) *Cache`

- [ ] **Step 1: Write the failing test**

Create `internal/semcache/wire_test.go`:

```go
package semcache

import (
	"context"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/embed"
)

func TestFromOptionsDisabledByDefault(t *testing.T) {
	c := FromOptions(Options{}, newFakeStore())
	if c.cfg.Enabled {
		t.Error("zero Options produced an enabled cache")
	}
}

func TestFromOptionsAppliesDefaults(t *testing.T) {
	c := FromOptions(Options{Enabled: true}, newFakeStore())
	if c.cfg.Threshold != DefaultThreshold {
		t.Errorf("Threshold = %v, want %v", c.cfg.Threshold, DefaultThreshold)
	}
	if c.cfg.MaxEntries != DefaultMaxEntries {
		t.Errorf("MaxEntries = %d, want %d", c.cfg.MaxEntries, DefaultMaxEntries)
	}
	if c.cfg.Model != embed.DefaultModel {
		t.Errorf("Model = %q, want %q", c.cfg.Model, embed.DefaultModel)
	}
}

func TestFromOptionsKeepsExplicitValues(t *testing.T) {
	c := FromOptions(Options{
		Enabled:    true,
		Model:      "custom-model",
		Threshold:  0.5,
		MaxEntries: 7,
		Timeout:    time.Second,
	}, newFakeStore())

	if c.cfg.Model != "custom-model" {
		t.Errorf("Model = %q, want custom-model", c.cfg.Model)
	}
	if c.cfg.Threshold != 0.5 {
		t.Errorf("Threshold = %v, want 0.5", c.cfg.Threshold)
	}
	if c.cfg.MaxEntries != 7 {
		t.Errorf("MaxEntries = %d, want 7", c.cfg.MaxEntries)
	}
}

// With no model listening, every call must miss quietly rather than fail.
func TestFromOptionsMissesWithNoModelRunning(t *testing.T) {
	c := FromOptions(Options{
		Enabled:  true,
		Endpoint: "http://127.0.0.1:1", // reserved port, never listening
		Timeout:  200 * time.Millisecond,
	}, newFakeStore())

	c.Store(context.Background(), Request{Text: "hello"}, "the response")
	if _, ok := c.Lookup(context.Background(), Request{Text: "hello"}); ok {
		t.Error("hit with no model running")
	}
	if c.Len() != 0 {
		t.Errorf("index len = %d, want 0 with no model running", c.Len())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/semcache/ -run FromOptions`
Expected: FAIL — `undefined: FromOptions`.

- [ ] **Step 3: Write the implementation**

Create `internal/semcache/wire.go`:

```go
package semcache

import (
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/embed"
)

// Options is the flat, config-file-shaped view of a Cache. The v0.2 gateway
// builds one of these from TOML and calls FromOptions; nothing else in the
// tree needs to know that embed and semcache are separate packages.
type Options struct {
	Enabled    bool
	Endpoint   string
	Model      string
	Threshold  float32
	MaxEntries int
	Timeout    time.Duration
}

// FromOptions builds a Cache backed by a live embedding client. Every unset
// field falls back to its documented default, and the zero Options yields a
// disabled cache.
func FromOptions(opts Options, store ccr.Store) *Cache {
	model := opts.Model
	if model == "" {
		model = embed.DefaultModel
	}
	client := embed.New(opts.Endpoint, model, opts.Timeout)
	return New(Config{
		Enabled:    opts.Enabled,
		Threshold:  opts.Threshold,
		MaxEntries: opts.MaxEntries,
		Model:      model,
	}, client, store)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/semcache/ -run FromOptions -v`
Expected: PASS, all four tests.

- [ ] **Step 5: Document the seam in the README**

Append this section to `README.md`:

```markdown
## Semantic cache (opt-in, off by default)

The gateway can return a stored provider response when a new request is
semantically close to one already seen. Closeness is measured by embedding both
requests with a local model.

The model runs in its own process and is reached over HTTP, so the binary stays
`CGO_ENABLED=0` and cross-compiles:

```bash
ollama serve
ollama pull nomic-embed-text
```

| Option | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Off unless explicitly turned on. |
| `endpoint` | `http://localhost:11434` | Where the embedding model listens. |
| `model` | `nomic-embed-text` | Part of the cache key; changing it invalidates stored entries. |
| `threshold` | `0.97` | Minimum cosine score for a hit. |
| `max_entries` | `2000` | Vector index bound. |
| `timeout` | `2s` | Embedding call budget; expiry means miss. |

**A cache hit returns a stored answer to a different question.** Every other
transform in headroom-go is reversible or information-preserving; this one is
not. Two requests about two different files can score above any threshold you
pick. Three rules follow, and the code enforces all three:

- A request carrying tool results is never cached and never served.
- A missing or failing model is a miss, never an error.
- The cache is off until you turn it on.

If Ollama is not running, every lookup misses and the gateway behaves exactly
as it does with the cache disabled.
```

- [ ] **Step 6: Run the full suite one more time**

Run: `CGO_ENABLED=0 go test -race ./... && gofmt -l . && go vet ./...`
Expected: tests PASS, `gofmt -l` prints nothing, `go vet` reports nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/semcache/wire.go internal/semcache/wire_test.go README.md
git commit -m "feat(semcache): FromOptions constructor and README section

Options is the flat, config-shaped view the v0.2 gateway builds from TOML,
so callers never need to know embed and semcache are separate packages.

The README states the trade plainly: a hit returns a stored answer to a
different question, which no threshold removes.

Refs: hr-47g.25"
```

---

## What this plan does not do

Named so a reviewer does not look for them:

- **No gateway wiring.** The v0.2 `livezone` gateway does not exist. `FromOptions` is the seam it will call.
- **No TOML parsing.** There is no config package yet. `Options` is a plain struct.
- **No CCR backend selection.** The caller passes any `ccr.Store`; `FromConfig` already builds those.
- **No streaming, warming, or eviction beyond the CCR TTL.** Spec section 10.
- **None of TOON, tool-schema stripping, tier routing, or generative compression.** Spec section 8 records why each was rejected.
