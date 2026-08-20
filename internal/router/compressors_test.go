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
	b.WriteString("warning: recompiling stale module cache\n")
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
