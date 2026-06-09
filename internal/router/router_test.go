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
