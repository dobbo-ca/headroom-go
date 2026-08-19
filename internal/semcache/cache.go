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
