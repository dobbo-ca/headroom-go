package compress

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// manyHunks builds a diff over n distinct files, each with one real change.
func manyHunks(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "diff --git a/f%d.go b/f%d.go\nindex 111..222 100644\n--- a/f%d.go\n+++ b/f%d.go\n@@ -1,1 +1,1 @@\n-old%d()\n+new%d()\n", i, i, i, i, i, i)
	}
	return b.String()
}

func TestDiffCompressorName(t *testing.T) {
	if got := NewDiffCompressor(DefaultDiffConfig()).Name(); got != "diff_compressor" {
		t.Errorf("Name = %q, want diff_compressor", got)
	}
}

func TestDiffCompressorAppliesToGitDiff(t *testing.T) {
	types := NewDiffCompressor(DefaultDiffConfig()).AppliesTo()
	if len(types) != 1 || types[0] != transform.GitDiff {
		t.Errorf("AppliesTo = %v, want [GitDiff]", types)
	}
}

func TestDefaultDiffConfigValue(t *testing.T) {
	if got := DefaultDiffConfig().MaxHunks; got != 40 {
		t.Errorf("MaxHunks = %d, want 40", got)
	}
}

func TestNewDiffCompressorFillsZeroConfig(t *testing.T) {
	if got := NewDiffCompressor(DiffConfig{}).cfg.MaxHunks; got != 40 {
		t.Errorf("MaxHunks = %d, want the default 40", got)
	}
}

func TestDiffCompressorDropsLockfileHunk(t *testing.T) {
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if strings.Contains(out.Output, "new/dep v1.1.0") {
		t.Error("output still holds the go.sum hunk body")
	}
	if !strings.Contains(out.Output, "new()") {
		t.Error("output lost the real main.go change")
	}
}

func TestDiffCompressorEmitsResolvableKey(t *testing.T) {
	store := newMapStore()
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.CacheKey == "" {
		t.Fatal("CacheKey is empty")
	}
	got, ok := store.Get(out.CacheKey)
	if !ok {
		t.Fatal("CacheKey does not resolve in the store")
	}
	if got != twoFileDiff {
		t.Error("stored payload is not the exact original")
	}
}

func TestDiffCompressorOutputCarriesTheMarker(t *testing.T) {
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(twoFileDiff, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, ccr.MarkerFor(out.CacheKey)) {
		t.Error("output does not contain the CCR marker for its own key")
	}
}

func TestDiffCompressorPreservesTrailingHeaderOnlySection(t *testing.T) {
	// A file section with no @@ block (binary diff, mode-only change, empty
	// file creation) must not vanish from the output. Pair it with a
	// dropped lockfile hunk so the run is genuinely lossy and must carry
	// the CCR marker in-band. TestDiffCompressorOutputCarriesTheMarker
	// alone would not catch a marker regression here: twoFileDiff has no
	// trailing header-only section to lose.
	in := twoFileDiff + "\ndiff --git a/img.png b/img.png\nindex 555..666 100644\nBinary files a/img.png and b/img.png differ"
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, "Binary files a/img.png and b/img.png differ") {
		t.Error("output lost the trailing binary-file section")
	}
	if out.BytesSaved <= 0 {
		t.Fatal("BytesSaved <= 0, want the dropped lockfile hunk to make this lossy")
	}
	if !strings.Contains(out.Output, ccr.MarkerFor(out.CacheKey)) {
		t.Error("lossy output does not carry the CCR marker for its own key")
	}
}

func TestDiffCompressorCapsHunkCount(t *testing.T) {
	in := manyHunks(100)
	cfg := DiffConfig{MaxHunks: 5}
	out, err := NewDiffCompressor(cfg).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got := strings.Count(out.Output, "@@ -1,1 +1,1 @@"); got != 5 {
		t.Errorf("kept %d hunks, want 5", got)
	}
	// The first five files survive, in file order.
	if !strings.Contains(out.Output, "new0()") {
		t.Error("output lost the first hunk")
	}
	if strings.Contains(out.Output, "new99()") {
		t.Error("output kept a hunk past the cap")
	}
}

func TestDiffCompressorNeverInflates(t *testing.T) {
	// One tiny real hunk: nothing to drop, and the marker would only add bytes.
	in := "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-a\n+b"
	store := newMapStore()
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input back", out.Output)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries; a no-op offload must not write", store.Len())
	}
}

func TestDiffCompressorNeverInflatesWhenDroppedHunkIsSmallerThanMarkerOverhead(t *testing.T) {
	// The kept hunk keeps the file headers; the second hunk is
	// whitespace-only noise and gets dropped. The dropped bytes are far
	// smaller than the "... N hunk(s) dropped" line plus the CCR marker,
	// so appending those must not be allowed to make the output longer
	// than the input.
	in := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-a\n+b\n@@ -9,1 +9,1 @@\n-x\n+ x\n"
	store := newMapStore()
	out, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(out.Output) > len(in) {
		t.Errorf("Output is %d bytes, input is %d: never-inflate violated", len(out.Output), len(in))
	}
	if out.BytesSaved < 0 {
		t.Errorf("BytesSaved = %d, want >= 0", out.BytesSaved)
	}
}

func TestDiffCompressorSkipsNonDiffInput(t *testing.T) {
	_, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply("this is not a diff", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrSkipped) {
		t.Errorf("err = %v, want ErrSkipped", err)
	}
}

func TestDiffCompressorRejectsEmptyInput(t *testing.T) {
	_, err := NewDiffCompressor(DefaultDiffConfig()).
		Apply("", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestDiffCompressorIsDeterministic(t *testing.T) {
	in := manyHunks(100)
	first, err := NewDiffCompressor(DiffConfig{MaxHunks: 5}).
		Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewDiffCompressor(DiffConfig{MaxHunks: 5}).
			Apply(in, transform.CompressionContext{}, newMapStore())
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output || next.CacheKey != first.CacheKey {
			t.Fatalf("run %d differs from the first run", i)
		}
	}
}

func TestDiffCompressorEstimateBloatGrowsWithHunkCount(t *testing.T) {
	c := NewDiffCompressor(DefaultDiffConfig())
	small := c.EstimateBloat(manyHunks(2))
	big := c.EstimateBloat(manyHunks(80))
	if !(small < big) {
		t.Errorf("EstimateBloat did not grow: small %v, big %v", small, big)
	}
	if big > 1 {
		t.Errorf("EstimateBloat = %v, must not exceed 1", big)
	}
}

func TestDiffCompressorEstimateBloatIsZeroWithoutHunks(t *testing.T) {
	if got := NewDiffCompressor(DefaultDiffConfig()).EstimateBloat("no hunks here"); got != 0 {
		t.Errorf("EstimateBloat = %v, want 0", got)
	}
}

func TestDiffCompressorConfidenceIsInRange(t *testing.T) {
	got := NewDiffCompressor(DefaultDiffConfig()).Confidence()
	if got <= 0 || got > 1 {
		t.Errorf("Confidence = %v, want a value in (0, 1]", got)
	}
}

func TestDiffCompressorSatisfiesTheInterface(t *testing.T) {
	var _ transform.OffloadTransform = NewDiffCompressor(DefaultDiffConfig())
}
