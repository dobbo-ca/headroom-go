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

// error-returning doubles: the pipeline must swallow these (skip-and-continue).

type errReformat struct{ types []transform.ContentType }

func (errReformat) Name() string                          { return "errReformat" }
func (e errReformat) AppliesTo() []transform.ContentType  { return e.types }
func (errReformat) Apply(string) (transform.ReformatOutput, error) {
	return transform.ReformatOutput{}, transform.ErrInternal
}

type errOffload struct{ types []transform.ContentType }

func (errOffload) Name() string                          { return "errOffload" }
func (e errOffload) AppliesTo() []transform.ContentType  { return e.types }
func (errOffload) EstimateBloat(string) float32          { return 0.9 }
func (errOffload) Confidence() float32                   { return 1 }
func (errOffload) Apply(string, transform.CompressionContext, ccr.Store) (transform.OffloadOutput, error) {
	return transform.OffloadOutput{}, transform.ErrSkipped
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

func TestOffloadFallbackRunsAllNonzeroBloat(t *testing.T) {
	// No reformat ran, so reformatRatio == 1.0 > 0.85 (fallback path). Every
	// offload with bloat > 0 runs, even the low-bloat one below the 0.5 threshold.
	hi := fakeOffload{name: "hi", types: []transform.ContentType{transform.JsonArray}, bloat: 0.9, out: "off", saved: 5, key: "k1"}
	lo := fakeOffload{name: "lo", types: []transform.ContentType{transform.JsonArray}, bloat: 0.1, out: "off2", saved: 5, key: "k2"}
	p := NewBuilder().WithOffload(hi).WithOffload(lo).Build()
	r := p.Run("input-with-no-reformat", transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if len(r.CacheKeys) != 2 {
		t.Fatalf("CacheKeys = %v, want both offloads via fallback path", r.CacheKeys)
	}
}

func TestOffloadPureBloatThresholdWhenFallbackOff(t *testing.T) {
	// A reformat cuts 100 -> 60 (ratio 0.6: not <=0.5 early-stop, but <=0.85 so
	// the fallback is OFF). Now only bloat >= 0.5 gates an offload: hi runs, lo
	// (bloat 0.3) does not.
	rf := fakeReformat{name: "rf", types: []transform.ContentType{transform.JsonArray}, out: strings.Repeat("z", 60), saved: 40}
	hi := fakeOffload{name: "hi", types: []transform.ContentType{transform.JsonArray}, bloat: 0.6, out: "off", saved: 5, key: "hi"}
	lo := fakeOffload{name: "lo", types: []transform.ContentType{transform.JsonArray}, bloat: 0.3, out: "off2", saved: 5, key: "lo"}
	p := NewBuilder().WithReformat(rf).WithOffload(hi).WithOffload(lo).Build()
	r := p.Run(strings.Repeat("z", 100), transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if len(r.CacheKeys) != 1 || r.CacheKeys[0] != "hi" {
		t.Fatalf("CacheKeys = %v, want [hi] (only bloat>=0.5 runs; fallback off at ratio 0.6)", r.CacheKeys)
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

func TestOffloadZeroSavedAddsNoCacheKey(t *testing.T) {
	// A high-bloat offload that saves nothing is skipped: no CacheKey, output
	// unchanged. (Contract: cache key recorded only on an accepted offload.)
	off := fakeOffload{name: "z", types: []transform.ContentType{transform.JsonArray}, bloat: 0.9, out: "shrunk", saved: 0, key: "nope"}
	p := NewBuilder().WithOffload(off).Build()
	in := "some json-ish input"
	r := p.Run(in, transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if len(r.CacheKeys) != 0 {
		t.Fatalf("CacheKeys = %v, want none (offload saved 0)", r.CacheKeys)
	}
	if r.Output != in {
		t.Fatalf("Output = %q, want unchanged %q", r.Output, in)
	}
}

func TestEmptyInputNoDivByZeroDeterministic(t *testing.T) {
	// originalLen == 0: the reformat early-stop guard (originalLen>0) and the
	// reformatRatio default (1.0) must avoid any division by zero, and the run
	// must stay deterministic.
	off := fakeOffload{name: "o", types: []transform.ContentType{transform.PlainText}, bloat: 0.9, out: "x", saved: 1, key: "k"}
	p := NewBuilder().WithOffload(off).Build()
	r := p.Run("", transform.PlainText, transform.CompressionContext{}, newStore(t))
	// Offload runs (bloat 0.9 >= 0.5) but saves 1 byte from empty; assert no panic
	// and a deterministic result rather than a specific output.
	a := p.Run("", transform.PlainText, transform.CompressionContext{}, newStore(t))
	if a.Output != r.Output || len(a.CacheKeys) != len(r.CacheKeys) {
		t.Fatalf("empty-input run not deterministic: %+v vs %+v", a, r)
	}
}

func TestReformatAndOffloadErrorsSkipped(t *testing.T) {
	// Both transforms return errors; the pipeline swallows them and passes the
	// input through unchanged, with no steps and no cache keys.
	er := errReformat{types: []transform.ContentType{transform.JsonArray}}
	eo := errOffload{types: []transform.ContentType{transform.JsonArray}}
	p := NewBuilder().WithReformat(er).WithOffload(eo).Build()
	in := "input that should survive errors"
	r := p.Run(in, transform.JsonArray, transform.CompressionContext{}, newStore(t))
	if r.Output != in {
		t.Fatalf("Output = %q, want passthrough %q on transform errors", r.Output, in)
	}
	if len(r.StepsApplied) != 0 || len(r.CacheKeys) != 0 || r.BytesSaved != 0 {
		t.Fatalf("errored transforms must contribute nothing, got %+v", r)
	}
}

func TestSaturatingAdd(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if got := saturatingAdd(5, 3); got != 8 {
		t.Errorf("saturatingAdd(5,3) = %d, want 8", got)
	}
	if got := saturatingAdd(maxInt, 1); got != maxInt {
		t.Errorf("saturatingAdd(maxInt,1) = %d, want %d (saturate)", got, maxInt)
	}
	if got := saturatingAdd(0, 0); got != 0 {
		t.Errorf("saturatingAdd(0,0) = %d, want 0", got)
	}
}
