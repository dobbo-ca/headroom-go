package compress

import (
	"strings"
	"testing"
)

func TestSearchParseWindowsDriveGuard(t *testing.T) {
	// C:\foo:10:bar must parse path "C:\foo", line 10, content "bar"
	in := "C:\\foo:10:bar\n"
	r := NewSearchCompressor().Compress(in, "", 0, newStore(t))
	if r.OriginalMatchCount != 1 {
		t.Fatalf("expected 1 match (windows-drive guard), got %d", r.OriginalMatchCount)
	}
}

func TestSearchCompressesClusteredMatches(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ { // 40 matches in 1 file -> per-file cap + adaptive
		b.WriteString("src/main.go:")
		b.WriteString(strings.Repeat("1", 1))
		b.WriteString(":func foo() {}\n")
	}
	in := b.String()
	st := newStore(t)
	r := NewSearchCompressor().Compress(in, "", 0, st)
	if r.CompressedMatchCount >= r.OriginalMatchCount {
		t.Fatalf("expected fewer matches kept, got %d/%d", r.CompressedMatchCount, r.OriginalMatchCount)
	}
	if r.CacheKey != "" {
		if v, ok := st.Get(r.CacheKey); !ok || v != in {
			t.Fatal("original must be retrievable")
		}
	}
}

func TestSearchEmptyPassThrough(t *testing.T) {
	r := NewSearchCompressor().Compress("not a search result at all\n", "", 0, newStore(t))
	if r.CompressionRatio != 1.0 {
		t.Fatalf("no parsed matches -> ratio 1.0, got %v", r.CompressionRatio)
	}
}
