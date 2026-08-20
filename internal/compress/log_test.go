package compress

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// mapStore is a minimal ccr.Store. The real backends need a blank import and a
// TTL; this keeps these tests about the compressor.
type mapStore struct{ m map[string]string }

func newMapStore() *mapStore { return &mapStore{m: map[string]string{}} }

func (s *mapStore) Put(hash, payload string) { s.m[hash] = payload }
func (s *mapStore) Get(hash string) (string, bool) {
	v, ok := s.m[hash]
	return v, ok
}
func (s *mapStore) Len() int { return len(s.m) }

var _ ccr.Store = (*mapStore)(nil)

// manyLines builds n distinct numbered lines, so no stage other than the
// middle offload can shorten it.
func manyLines(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("line %d unique content here", i)
	}
	return strings.Join(parts, "\n")
}

func TestLogCompressorName(t *testing.T) {
	if got := NewLogCompressor(DefaultLogConfig()).Name(); got != "log_compressor" {
		t.Errorf("Name = %q, want log_compressor", got)
	}
}

func TestLogCompressorAppliesToBuildOutput(t *testing.T) {
	types := NewLogCompressor(DefaultLogConfig()).AppliesTo()
	if len(types) != 1 || types[0] != transform.BuildOutput {
		t.Errorf("AppliesTo = %v, want [BuildOutput]", types)
	}
}

func TestDefaultLogConfigValues(t *testing.T) {
	cfg := DefaultLogConfig()
	if cfg.HeadLines != 50 || cfg.TailLines != 50 || cfg.MinLinesToOffload != 200 {
		t.Errorf("DefaultLogConfig = %+v, want {50 50 200}", cfg)
	}
}

func TestNewLogCompressorFillsZeroConfig(t *testing.T) {
	c := NewLogCompressor(LogConfig{})
	if c.cfg != DefaultLogConfig() {
		t.Errorf("cfg = %+v, want the defaults %+v", c.cfg, DefaultLogConfig())
	}
}

func TestOffloadMiddleKeepsHeadAndTail(t *testing.T) {
	in := manyLines(20)
	got := offloadMiddle(in, 3, 2, 10, "<<MARK>>")
	lines := strings.Split(got, "\n")
	if len(lines) != 6 { // 3 head + marker + 2 tail
		t.Fatalf("got %d lines, want 6: %q", len(lines), got)
	}
	if lines[0] != "line 0 unique content here" {
		t.Errorf("first line = %q", lines[0])
	}
	if lines[3] != "<<MARK>>" {
		t.Errorf("marker line = %q, want <<MARK>>", lines[3])
	}
	if lines[5] != "line 19 unique content here" {
		t.Errorf("last line = %q", lines[5])
	}
}

func TestOffloadMiddleBelowThresholdIsNoOp(t *testing.T) {
	in := manyLines(5)
	if got := offloadMiddle(in, 3, 2, 10, "<<MARK>>"); got != in {
		t.Errorf("offloadMiddle shortened input below the threshold")
	}
}

func TestOffloadMiddleNoOpWhenHeadPlusTailCoversInput(t *testing.T) {
	in := manyLines(12)
	// head+tail >= len(lines): there is no middle to remove.
	if got := offloadMiddle(in, 8, 8, 10, "<<MARK>>"); got != in {
		t.Errorf("offloadMiddle changed input with no middle to drop")
	}
}

func TestLogCompressorApplyEmitsResolvableKey(t *testing.T) {
	in := manyLines(500)
	store := newMapStore()

	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, store)
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
	if got != in {
		t.Error("stored payload is not the exact original")
	}
}

func TestLogCompressorApplyShortensAndReportsSavings(t *testing.T) {
	in := manyLines(500)
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(out.Output) >= len(in) {
		t.Fatalf("output len %d not shorter than input len %d", len(out.Output), len(in))
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Errorf("BytesSaved = %d, want %d", out.BytesSaved, len(in)-len(out.Output))
	}
}

func TestLogCompressorOutputCarriesTheMarker(t *testing.T) {
	in := manyLines(500)
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, ccr.MarkerFor(out.CacheKey)) {
		t.Error("output does not contain the CCR marker for its own key")
	}
}

func TestLogCompressorNeverInflates(t *testing.T) {
	// Short, already-minimal input: no stage can shorten it.
	in := "a\nb\nc"
	store := newMapStore()
	out, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, store)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input %q back", out.Output, in)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries; a no-op offload must not write", store.Len())
	}
}

func TestLogCompressorRejectsEmptyInput(t *testing.T) {
	_, err := NewLogCompressor(DefaultLogConfig()).Apply("", transform.CompressionContext{}, newMapStore())
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestLogCompressorIsDeterministic(t *testing.T) {
	in := manyLines(500)
	first, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 10; i++ {
		next, err := NewLogCompressor(DefaultLogConfig()).Apply(in, transform.CompressionContext{}, newMapStore())
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output || next.CacheKey != first.CacheKey {
			t.Fatalf("run %d differs from the first run", i)
		}
	}
}

func TestLogCompressorEstimateBloatGrowsWithLineCount(t *testing.T) {
	c := NewLogCompressor(DefaultLogConfig())
	small := c.EstimateBloat(manyLines(10))
	big := c.EstimateBloat(manyLines(400))
	if !(small < big) {
		t.Errorf("EstimateBloat did not grow: small %v, big %v", small, big)
	}
	if big > 1 {
		t.Errorf("EstimateBloat = %v, must not exceed 1", big)
	}
	if small < 0 {
		t.Errorf("EstimateBloat = %v, must not be negative", small)
	}
}

func TestLogCompressorEstimateBloatIsZeroForOneLine(t *testing.T) {
	if got := NewLogCompressor(DefaultLogConfig()).EstimateBloat("single line"); got != 0 {
		t.Errorf("EstimateBloat = %v, want 0", got)
	}
}

func TestLogCompressorConfidenceIsInRange(t *testing.T) {
	got := NewLogCompressor(DefaultLogConfig()).Confidence()
	if got <= 0 || got > 1 {
		t.Errorf("Confidence = %v, want a value in (0, 1]", got)
	}
}

func TestLogCompressorSatisfiesTheInterface(t *testing.T) {
	var _ transform.OffloadTransform = NewLogCompressor(DefaultLogConfig())
}
