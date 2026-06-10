package router

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func st(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaultCompressesJSONArray(t *testing.T) {
	r := NewDefault()
	in := "[ {\n \"a\": 1\n}, {\n \"a\": 2\n} ]"
	res := r.Compress(in, transform.CompressionContext{}, st(t))
	if len(res.Output) >= len(in) {
		t.Fatalf("JSON array should be minified: %q", res.Output)
	}
}

func TestDefaultCompressesLargeDiff(t *testing.T) {
	r := NewDefault()
	var b strings.Builder
	b.WriteString("diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n")
	for i := 0; i < 60; i++ {
		b.WriteString("@@ -1 +1 @@\n+x v1 h1:y\n")
	}
	in := b.String()
	res := r.Compress(in, transform.CompressionContext{}, st(t))
	if len(res.Output) >= len(in) || len(res.CacheKeys) == 0 {
		t.Fatalf("lockfile diff should offload; output=%d in=%d keys=%v", len(res.Output), len(in), res.CacheKeys)
	}
}

func TestDefaultPlainTextPassthrough(t *testing.T) {
	r := NewDefault()
	in := "just some prose with no structure at all, several words long here ok"
	if got := r.Compress(in, transform.CompressionContext{}, st(t)).Output; got != in {
		t.Fatalf("plain text must pass through: %q", got)
	}
}

func TestDefaultDeterministic(t *testing.T) {
	r := NewDefault()
	in := "[ {\n \"a\": 1\n}, {\n \"a\": 2\n} ]"
	a := r.Compress(in, transform.CompressionContext{}, st(t)).Output
	b := r.Compress(in, transform.CompressionContext{}, st(t)).Output
	if a != b {
		t.Fatalf("non-deterministic: %q != %q", a, b)
	}
}
