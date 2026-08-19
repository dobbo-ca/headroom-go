package semcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// fakeStore is a minimal in-memory ccr.Store. The real backends need a blank
// import and a TTL; this keeps the cache tests about the cache.
type fakeStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]string{}} }

func (f *fakeStore) Put(hash, payload string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[hash] = payload
}
func (f *fakeStore) Get(hash string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[hash]
	return v, ok
}
func (f *fakeStore) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}

var _ ccr.Store = (*fakeStore)(nil)

// fixedEmbedder returns a preset vector per text, so tests control similarity
// exactly instead of depending on a real model.
type fixedEmbedder struct {
	vecs  map[string][]float32
	calls atomic.Int64
	err   error
}

func (f *fixedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	f.calls.Add(1)
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
	if e.calls.Load() != 0 {
		t.Errorf("embedder called %d times while disabled, want 0", e.calls.Load())
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
	if e.calls.Load() != 0 {
		t.Errorf("embedder called %d times storing tool results, want 0", e.calls.Load())
	}
	if _, ok := c.Lookup(context.Background(), Request{Text: "hello", HasToolResults: true}); ok {
		t.Error("request with tool results returned a hit")
	}
	if e.calls.Load() != 0 {
		t.Errorf("embedder called %d times looking up tool results, want 0", e.calls.Load())
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
	if e.calls.Load() != 0 {
		t.Errorf("embedder called %d times on empty text, want 0", e.calls.Load())
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
