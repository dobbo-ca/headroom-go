package offloads

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestJsonOffloadEmptyBloatZero(t *testing.T) {
	if got := NewJsonOffload().EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestJsonOffloadConfidence(t *testing.T) {
	if c := NewJsonOffload().Confidence(); c != 0.85 {
		t.Fatalf("Confidence = %v, want 0.85", c)
	}
}

func TestJsonOffloadNonArrayBloatZero(t *testing.T) {
	if got := NewJsonOffload().EstimateBloat(`{"a":1}`); got != 0 {
		t.Fatalf("non-array input -> 0 bloat, got %v", got)
	}
}

func TestJsonOffloadArrayBloat(t *testing.T) {
	// 10-row compact array => 9 separators >= min_array_rows-1 (4) => >0.
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"a":1}`)
	}
	b.WriteString("]")
	if got := NewJsonOffload().EstimateBloat(b.String()); got <= 0 {
		t.Fatalf("10-row array should look bloated, got %v", got)
	}
}

func TestJsonOffloadPassthroughSkips(t *testing.T) {
	// Plan-2 default crusher is passthrough -> always Skips, nothing stored.
	st := store(t)
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"a":1}`)
	}
	b.WriteString("]")
	_, err := NewJsonOffload().Apply(b.String(), transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("passthrough crusher -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

func TestJsonOffloadAppliesTo(t *testing.T) {
	at := NewJsonOffload().AppliesTo()
	if len(at) != 1 || at[0] != transform.JsonArray {
		t.Fatalf("AppliesTo = %v, want [JsonArray]", at)
	}
}

var _ transform.OffloadTransform = (*JsonOffload)(nil)
