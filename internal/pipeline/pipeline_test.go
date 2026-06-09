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
	name  string
	types []transform.ContentType
	out   string
	saved int
}

func (f fakeReformat) Name() string                       { return f.name }
func (f fakeReformat) AppliesTo() []transform.ContentType { return f.types }
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
