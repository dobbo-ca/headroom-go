package livezone

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
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

// repetitiveJSONBlock is a JSON array the default router reliably shrinks,
// and is well above BlockByteThreshold.
func repetitiveJSONBlock() string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`{"id": 1, "name": "alpha beta", "status": "ok", "latency": 12}`)
	}
	b.WriteString("]")
	return b.String()
}

// flatCounter reports the same token count for every string, so the I5 gate
// sees tokensAfter == tokensBefore and must reject.
type flatCounter struct{}

func (flatCounter) CountText(string) int       { return 100 }
func (flatCounter) Backend() tokenizer.Backend { return tokenizer.BackendEstimation }

// An accepted block's replacement carries the marker, and the token count the
// gate used is the count of that exact replacement — i.e. the marker was
// injected BEFORE the gate and cannot escape the accounting.
func TestCompressBlockMarkerIsInsideTheGatedText(t *testing.T) {
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	store := newMapStore()
	text := repetitiveJSONBlock()

	res := compressBlock(text, Options{Router: router.NewDefault(), Store: store}, tok)
	if !res.accepted {
		t.Fatalf("router failed to shrink a repetitive JSON array: %+v", res)
	}
	if res.cacheKey != ccr.ComputeKey([]byte(text)) {
		t.Errorf("cacheKey = %q, want the key of the ORIGINAL text", res.cacheKey)
	}
	if !strings.HasSuffix(res.replacement, ccr.MarkerFor(res.cacheKey)) {
		t.Errorf("replacement must end with the CCR marker (I6): %q", res.replacement)
	}
	if got := tok.CountText(res.replacement); got != res.tokensAfter {
		t.Errorf("tokensAfter = %d but the replacement counts %d: the marker escaped the I5 gate",
			res.tokensAfter, got)
	}
	if res.tokensBefore != tok.CountText(text) {
		t.Errorf("tokensBefore = %d, want the count of the original text %d",
			res.tokensBefore, tok.CountText(text))
	}
	// Scoped to the live-zone key: a compressor in the pipeline may keep its
	// own CCR entry under its own key, which is not this package's business.
	if _, ok := store.Get(res.cacheKey); ok {
		t.Error("compressBlock stored the original; only Dispatch may store, after the gate")
	}
}

// Every marker Dispatch emits resolves: the store holds the original under
// exactly the hash written into the body.
func TestDispatchStoresOriginalUnderTheEmittedMarker(t *testing.T) {
	text := repetitiveJSONBlock()
	store := newMapStore()
	body := userBodyWithText(t, text)

	res := Dispatch(body, Options{Router: router.NewDefault(), Store: store, FrozenCount: 0})
	if !res.Applied {
		t.Fatalf("Dispatch did not rewrite: %+v", res.Reason)
	}

	hash := markerHashIn(t, string(res.Body))
	got, ok := store.Get(hash)
	if !ok {
		t.Fatalf("marker %q in the body has no store entry: it cannot be resolved", hash)
	}
	if got != text {
		t.Errorf("store payload does not round-trip to the original block text")
	}
}

// A block rejected by the I5 gate leaves no orphan store entry: nothing may
// be written before the gate accepts.
func TestDispatchRejectedBlockLeavesNoOrphanEntry(t *testing.T) {
	store := newMapStore()
	body := userBodyWithText(t, repetitiveJSONBlock())

	res := Dispatch(body, Options{
		Router: router.NewDefault(), Store: store, Tokenizer: flatCounter{}, FrozenCount: 0})
	if res.Applied {
		t.Fatalf("equal token counts must be rejected by I5")
	}
	if res.Reason != ReasonAllRejected {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonAllRejected)
	}
	if _, ok := store.Get(ccr.ComputeKey([]byte(repetitiveJSONBlock()))); ok {
		t.Error("a rejected block left its original in the store; it must be stored only after the gate accepts")
	}
}

// userBodyWithText embeds text as the latest user message's single text block.
func userBodyWithText(t *testing.T, text string) []byte {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return []byte(`{"model":"claude-3-5-sonnet-20241022","system":"you are helpful",` +
		`"messages":[{"role":"user","content":[{"type":"text","text":` + string(quoted) + `}]}]}`)
}

// markerHashIn extracts the single <<ccr:HASH>> hash present in s.
func markerHashIn(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "<<ccr:")
	if i < 0 {
		t.Fatalf("no CCR marker in %q", s)
	}
	rest := s[i+len("<<ccr:"):]
	j := strings.Index(rest, ">>")
	if j < 0 {
		t.Fatalf("unterminated CCR marker in %q", s)
	}
	return rest[:j]
}
