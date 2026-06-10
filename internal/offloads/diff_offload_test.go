package offloads

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestDiffOffloadEmptyBloatZero(t *testing.T) {
	do := NewDiffOffload(compress.NewDiffCompressor())
	if got := do.EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestDiffOffloadConfidence(t *testing.T) {
	if c := NewDiffOffload(compress.NewDiffCompressor()).Confidence(); c != 0.85 {
		t.Fatalf("Confidence = %v, want 0.85", c)
	}
}

func TestDiffOffloadShortInputSkips(t *testing.T) {
	st := store(t)
	do := NewDiffOffload(compress.NewDiffCompressor())
	in := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	_, err := do.Apply(in, transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("short diff -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

func TestDiffOffloadContextHeavyBloat(t *testing.T) {
	// >50 lines, mostly context -> bloat > 0.
	var b strings.Builder
	b.WriteString("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n")
	for i := 0; i < 60; i++ {
		b.WriteString(" context line\n")
	}
	b.WriteString("+added\n")
	if got := NewDiffOffload(compress.NewDiffCompressor()).EstimateBloat(b.String()); got <= 0 {
		t.Fatalf("context-heavy diff should look bloated, got %v", got)
	}
}

var _ transform.OffloadTransform = (*DiffOffload)(nil)
