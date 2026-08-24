package livezone

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// mapStore is a minimal ccr.Store for tests.
type mapStore struct{ m map[string]string }

func newMapStore() *mapStore { return &mapStore{m: map[string]string{}} }

func (s *mapStore) Put(hash, payload string) { s.m[hash] = payload }
func (s *mapStore) Get(hash string) (string, bool) {
	v, ok := s.m[hash]
	return v, ok
}
func (s *mapStore) Len() int { return len(s.m) }

func TestInjectCCRMarkerAppendsAtEnd(t *testing.T) {
	store := newMapStore()
	original := "the original text"
	out, hash := injectCCRMarker(original, "compressed", store)

	if hash != ccr.ComputeKey([]byte(original)) {
		t.Errorf("hash = %q, want the key of the ORIGINAL text", hash)
	}
	marker := ccr.MarkerFor(hash)
	if !strings.HasSuffix(out, marker) {
		t.Errorf("marker must be appended at the end (I6): %q", out)
	}
	if !strings.HasPrefix(out, "compressed") {
		t.Errorf("compressed text must be preserved at the start: %q", out)
	}
	if out != "compressed\n"+marker {
		t.Errorf("out = %q, want exactly one newline before the marker", out)
	}
}

// Exactly one newline separates the text from the marker, whether or not the
// compressed text already ends in one.
func TestInjectCCRMarkerSingleNewline(t *testing.T) {
	store := newMapStore()
	out, hash := injectCCRMarker("orig", "compressed\n", store)
	if out != "compressed\n"+ccr.MarkerFor(hash) {
		t.Errorf("out = %q, want no doubled newline", out)
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("doubled newline before the marker: %q", out)
	}
}

// injectCCRMarker must NOT write to the store; the caller does that only
// after the I5 gate accepts, so a rejected block leaves no orphan entry.
func TestInjectCCRMarkerDoesNotWriteToStore(t *testing.T) {
	store := newMapStore()
	injectCCRMarker("orig", "compressed", store)
	if store.Len() != 0 {
		t.Errorf("store has %d entries; injectCCRMarker must not write", store.Len())
	}
}

func TestInjectCCRMarkerNilStoreIsNoOp(t *testing.T) {
	out, hash := injectCCRMarker("orig", "compressed", nil)
	if out != "compressed" {
		t.Errorf("out = %q, want the compressed text unchanged", out)
	}
	if hash != "" {
		t.Errorf("hash = %q, want empty with no store", hash)
	}
}

// I5: a replacement is kept only when its token count is STRICTLY lower.
func TestI5RejectsWhenTokensDoNotShrink(t *testing.T) {
	tests := []struct {
		name         string
		before       int
		after        int
		wantAccepted bool
	}{
		{"strictly smaller is accepted", 100, 99, true},
		{"equal is rejected", 100, 100, false},
		{"larger is rejected", 100, 101, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := acceptsI5(tt.before, tt.after); got != tt.wantAccepted {
				t.Errorf("acceptsI5(%d,%d) = %v, want %v", tt.before, tt.after, got, tt.wantAccepted)
			}
		})
	}
}

// With no Router wired, compressBlock must decline rather than panic.
func TestCompressBlockNilRouter(t *testing.T) {
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	res := compressBlock(strings.Repeat("x", 1000), Options{}, tok)
	if res.accepted {
		t.Error("accepted with no Router wired")
	}
}

// A compressor that returns the input unchanged must be declined, and must
// not be counted as a rejection-by-tokens.
func TestCompressBlockNoOpWhenOutputEqualsInput(t *testing.T) {
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	res := compressBlock("unchanged", Options{}, tok)
	if res.accepted {
		t.Error("accepted a no-op")
	}
	if res.action == "compressed" {
		t.Errorf("action = %q, want a non-compressed action", res.action)
	}
}
