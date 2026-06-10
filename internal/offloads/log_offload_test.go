package offloads

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestLogOffloadEmptyBloatZero(t *testing.T) {
	lo := NewLogOffload(compress.NewLogCompressor())
	if got := lo.EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestLogOffloadConfidence(t *testing.T) {
	if c := NewLogOffload(compress.NewLogCompressor()).Confidence(); c != 0.85 {
		t.Fatalf("Confidence = %v, want 0.85", c)
	}
}

func TestLogOffloadShortInputSkips(t *testing.T) {
	// Below the wrapped compressor's min_lines_for_ccr: no key -> ErrSkipped.
	st := store(t)
	lo := NewLogOffload(compress.NewLogCompressor())
	in := strings.Repeat("2024-01-01 INFO something happened\n", 5)
	_, err := lo.Apply(in, transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("short input -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

func TestLogOffloadAppliesTo(t *testing.T) {
	at := NewLogOffload(compress.NewLogCompressor()).AppliesTo()
	if len(at) != 1 || at[0] != transform.BuildOutput {
		t.Fatalf("AppliesTo = %v, want [BuildOutput]", at)
	}
}

var _ transform.OffloadTransform = (*LogOffload)(nil)
