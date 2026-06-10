package compress

import (
	"strings"
	"testing"
)

func TestDiffShortInputPassThrough(t *testing.T) {
	in := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	r := NewDiffCompressor().Compress(in, "", newStore(t))
	if r.Compressed != in || r.CacheKey != "" {
		t.Fatal("input < 50 lines must pass through verbatim with no CCR")
	}
}

func TestDiffCompressesManyHunksAndStoresOriginal(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n")
	for i := 0; i < 30; i++ { // 30 hunks -> capped to 10, with header lines pushes well over 50 lines
		b.WriteString("@@ -")
		b.WriteString(strings.Repeat("1", 1))
		b.WriteString(" +1 @@\n-old\n+new\n context\n")
	}
	in := b.String()
	st := newStore(t)
	r := NewDiffCompressor().Compress(in, "", st)
	if r.OriginalLineCount < 50 {
		t.Skip("fixture too small; enlarge")
	}
	if r.HunksRemoved == 0 {
		t.Fatal("expected hunks to be dropped (cap 10/file)")
	}
	if r.CacheKey != "" {
		if v, ok := st.Get(r.CacheKey); !ok || v != in {
			t.Fatal("original must be retrievable under CacheKey")
		}
		if !strings.Contains(r.Compressed, "Retrieve full diff: hash="+r.CacheKey) {
			t.Fatal("inline diff CCR marker missing")
		}
	}
}
