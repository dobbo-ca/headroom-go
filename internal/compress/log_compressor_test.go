package compress

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
)

// newStore builds an in-memory CCR store for compress-engine tests. Tasks 10/11
// reuse this helper; do NOT redefine it in their test files.
func newStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLogCompressorDropsLowValueAndStoresOriginal(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString("DEBUG noisy heartbeat tick\n")
	}
	b.WriteString("ERROR database connection refused\n")
	in := b.String()
	st := newStore(t)
	r := NewLogCompressor().Compress(in, st)
	if r.CompressedLineCount >= r.OriginalLineCount {
		t.Fatalf("expected compression, got %d/%d", r.CompressedLineCount, r.OriginalLineCount)
	}
	if !strings.Contains(r.Compressed, "ERROR database connection refused") {
		t.Fatal("must keep the error line")
	}
	if r.CacheKey == "" {
		t.Fatal("expected CCR key (80+ lines, ratio < 0.5)")
	}
	if v, ok := st.Get(r.CacheKey); !ok || v != in {
		t.Fatal("original must be retrievable from the store under CacheKey")
	}
	if !strings.Contains(r.Compressed, "Retrieve more: hash="+r.CacheKey) {
		t.Fatal("inline CCR marker missing")
	}
}

func TestLogCompressorShortInputNoCCR(t *testing.T) {
	r := NewLogCompressor().Compress("INFO a\nINFO b\nERROR c\n", newStore(t))
	if r.CacheKey != "" {
		t.Fatal("inputs < 50 lines never emit a CCR marker")
	}
}

func TestLogCompressorWordBoundaryLevel(t *testing.T) {
	c := NewLogCompressor()
	// "errorless" must NOT classify as Error (word boundary).
	if got := c.classifyLevel("errorless heartbeat"); got != levelUnknown {
		t.Errorf("'errorless' must not match Error, got level %v", got)
	}
	// "ERROR_CODE" must NOT match (trailing word byte '_').
	if got := c.classifyLevel("ERROR_CODE present"); got != levelUnknown {
		t.Errorf("'ERROR_CODE' must not match Error, got level %v", got)
	}
	// A standalone ERROR token matches.
	if got := c.classifyLevel("ERROR: disk full"); got != levelError {
		t.Errorf("'ERROR' must match Error, got level %v", got)
	}
	// Precedence: Error beats Warn on the same line.
	if got := c.classifyLevel("WARNING then ERROR"); got != levelError {
		t.Errorf("Error must take precedence over Warn, got level %v", got)
	}
}

func TestLogCompressorRatioGate(t *testing.T) {
	// 60 lines that are all distinct errors -> low compression (ratio >= 0.5),
	// so even though originalLineCount >= 50 the CCR marker must NOT fire.
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("ERROR unique failure number ")
		b.WriteString(strings.Repeat("x", i%5+1))
		b.WriteByte('\n')
	}
	r := NewLogCompressor().Compress(b.String(), newStore(t))
	if r.Ratio < 0.5 && r.CacheKey == "" {
		t.Fatalf("ratio < 0.5 should have produced a CacheKey, ratio=%v", r.Ratio)
	}
	if r.Ratio >= 0.5 && r.CacheKey != "" {
		t.Fatalf("ratio >= 0.5 must not emit a CCR marker, ratio=%v key=%q", r.Ratio, r.CacheKey)
	}
}

func TestLogCompressorDeterministicAtCap(t *testing.T) {
	// 200 distinct lines that all score identically (each "===" prefix => summary,
	// level Unknown => 0.1 base + 0.4 summary boost = 0.5) and are all kept, so
	// the selected set exceeds the cap (max_total_lines=100) entirely at a score
	// tie. The map-gathered `selected` slice has randomized order, so without a
	// total-order tie-break the cap truncation would emit a non-deterministic body
	// (I4 violation). Compress repeatedly and assert byte-identical output.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("=== distinct ")
		b.WriteString(strings.Repeat("z", i%50+1))
		b.WriteString(strings.Repeat("q", (i/3)%7+3))
		b.WriteByte('\n')
	}
	in := b.String()
	first := NewLogCompressor().Compress(in, newStore(t)).Compressed
	for n := 0; n < 100; n++ {
		got := NewLogCompressor().Compress(in, newStore(t)).Compressed
		if got != first {
			t.Fatalf("compression output is non-deterministic at the cap (run %d)", n)
		}
	}
}

func TestLogCompressorWarningDedup(t *testing.T) {
	// Warnings differing only in a decimal number normalize (\d+ -> N) to the
	// same dedup key and collapse to one kept warning (silent dedup, no
	// "repeated" literal). NOTE: the strict regex order is \d+->N FIRST, which
	// (faithful to upstream) mangles "0x" in hex addresses before the hex regex
	// can fire — so hex addresses do NOT normalize. The fixture therefore varies
	// only a decimal field.
	var b strings.Builder
	// pad with low-value lines so we have >50 lines and clear compression.
	for i := 0; i < 60; i++ {
		b.WriteString("DEBUG tick\n")
	}
	b.WriteString("WARNING: retry attempt 1 for worker\n")
	b.WriteString("WARNING: retry attempt 2 for worker\n")
	b.WriteString("WARNING: retry attempt 3 for worker\n")
	out := NewLogCompressor().Compress(b.String(), newStore(t)).Compressed
	if strings.Contains(out, "repeated") {
		t.Fatal("dedup must be silent; no 'repeated' literal")
	}
	n := strings.Count(out, "WARNING: retry attempt")
	if n != 1 {
		t.Fatalf("expected exactly 1 deduped warning, got %d", n)
	}
}
