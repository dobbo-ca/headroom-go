package offloads

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestSearchOffloadEmptyBloatZero(t *testing.T) {
	so := NewSearchOffload(compress.NewSearchCompressor())
	if got := so.EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestSearchOffloadConfidence(t *testing.T) {
	if c := NewSearchOffload(compress.NewSearchCompressor()).Confidence(); c != 0.85 {
		t.Fatalf("Confidence = %v, want 0.85", c)
	}
}

func TestSearchOffloadAppliesTo(t *testing.T) {
	at := NewSearchOffload(compress.NewSearchCompressor()).AppliesTo()
	if len(at) != 1 || at[0] != transform.SearchResults {
		t.Fatalf("AppliesTo = %v, want [SearchResults]", at)
	}
}

func TestSearchOffloadClusteredBloat(t *testing.T) {
	// Many matches clustered into few files -> high bloat.
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("src/main.go:42:some matched content here\n")
	}
	for i := 0; i < 20; i++ {
		b.WriteString("src/util.go:7:another match line\n")
	}
	if got := NewSearchOffload(compress.NewSearchCompressor()).EstimateBloat(b.String()); got <= 0 {
		t.Fatalf("clustered matches should look bloated, got %v", got)
	}
}

func TestSearchOffloadName(t *testing.T) {
	if n := NewSearchOffload(compress.NewSearchCompressor()).Name(); n != "search_offload" {
		t.Fatalf("Name = %q, want search_offload", n)
	}
}

var _ transform.OffloadTransform = (*SearchOffload)(nil)
