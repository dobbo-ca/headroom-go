package livezone

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/dobbo-ca/headroom-go/internal/transform"
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
	res := compressBlock(strings.Repeat("x", 1000), transform.CompressionContext{}, Options{}, tok)
	if res.accepted {
		t.Error("accepted with no Router wired")
	}
}

// A nil Store must decline before the router's offload transforms are
// reached: they stash the original under the store unconditionally, so a
// nil Store there would panic rather than raise ok=false.
func TestCompressBlockNilStoreDeclinesInsteadOfPanicking(t *testing.T) {
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	res := compressBlock(repetitiveJSONBlock(), transform.CompressionContext{}, Options{Router: router.NewDefault()}, tok)
	if res.accepted {
		t.Error("accepted with no Store wired")
	}
	if res.action != "no_op" {
		t.Errorf("action = %q, want no_op", res.action)
	}
}

// A compressor that returns the input unchanged must be declined, and must
// not be counted as a rejection-by-tokens.
func TestCompressBlockNoOpWhenOutputEqualsInput(t *testing.T) {
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	res := compressBlock("unchanged", transform.CompressionContext{}, Options{}, tok)
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

	res := compressBlock(text, transform.CompressionContext{}, Options{Router: router.NewDefault(), Store: store}, tok)
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

// Dispatch's reported accounting is the caller's only view of what the I5
// gate decided, so it is pinned end to end: the per-block outcome carries the
// block's own counts and the key the marker in the body was built from, and
// the Result totals are their sum, in the right direction.
func TestDispatchReportsTokenAccounting(t *testing.T) {
	text := repetitiveJSONBlock()
	tok := tokenizer.EstimatingCounter{CharsPerToken: 4.0}
	body := userBodyWithText(t, text)

	res := Dispatch(body, Options{
		Router: router.NewDefault(), Store: newMapStore(), Tokenizer: tok, FrozenCount: 0})
	if !res.Applied {
		t.Fatalf("Dispatch did not rewrite: %q", res.Reason)
	}
	if len(res.Blocks) != 1 {
		t.Fatalf("got %d block outcomes, want 1: %+v", len(res.Blocks), res.Blocks)
	}
	b := res.Blocks[0]
	if b.Action != "compressed" {
		t.Errorf("Action = %q, want compressed", b.Action)
	}
	if b.CacheKey != markerHashIn(t, string(res.Body)) {
		t.Errorf("CacheKey = %q, want the hash of the marker in the body %q",
			b.CacheKey, markerHashIn(t, string(res.Body)))
	}
	if b.TokensBefore != tok.CountText(text) {
		t.Errorf("block TokensBefore = %d, want the original's count %d",
			b.TokensBefore, tok.CountText(text))
	}
	if b.TokensAfter <= 0 || b.TokensAfter >= b.TokensBefore {
		t.Errorf("block TokensAfter = %d, want a positive count below %d",
			b.TokensAfter, b.TokensBefore)
	}
	if res.TokensBefore != b.TokensBefore || res.TokensAfter != b.TokensAfter {
		t.Errorf("Result totals = %d->%d, want the block's %d->%d",
			res.TokensBefore, res.TokensAfter, b.TokensBefore, b.TokensAfter)
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
	// Checking the BLAKE3 key alone is not enough. The router's offload
	// transforms key their own copy of the original with ComputeKeyMD5, so a
	// hash-specific assertion passes while the store still holds an orphan.
	// Count entries instead: after a rejection the store must be untouched.
	if n := store.Len(); n != 0 {
		t.Errorf("store holds %d entries after a rejected block; want 0 (orphans no marker on the wire can name)", n)
	}
}

// Every retrieval hash the compressed body carries must resolve. The live-zone
// dispatcher appends <<ccr:HASH>>, and the compressors append their own
// "hash=HASH" note; both travel upstream, so both must be backed by a stored
// original. Asserting over the hashes actually present in the body means a new
// marker format cannot be added without its store write.
func TestDispatchEveryHashInTheBodyResolves(t *testing.T) {
	store := newMapStore()
	text := repetitiveJSONBlock()
	body := userBodyWithText(t, text)

	res := Dispatch(body, Options{
		Router: router.NewDefault(), Store: store,
		Tokenizer: tokenizer.GetTokenizer(DefaultModel), FrozenCount: 0})
	if !res.Applied {
		t.Fatalf("fixture did not compress (reason %q); it cannot exercise marker resolution", res.Reason)
	}

	hashes := hashesIn(string(res.Body))
	if len(hashes) == 0 {
		t.Fatal("the compressed body carries no retrieval hash at all")
	}
	for _, h := range hashes {
		if _, ok := store.Get(h); !ok {
			t.Errorf("hash %s appears in the body sent upstream but resolves to nothing", h)
		}
	}
	// SmartCrusher keys individual crushed cells, so not every hash on the
	// wire names the whole block. The dispatcher's own marker always does.
	whole, ok := store.Get(ccr.ComputeKey([]byte(text)))
	if !ok {
		t.Fatal("the dispatcher's <<ccr:> marker does not resolve")
	}
	if whole != text {
		t.Errorf("the dispatcher's marker resolves to %d bytes; want the %d-byte original",
			len(whole), len(text))
	}
}

// hashRe matches both marker shapes the wire carries: the dispatcher's
// <<ccr:HASH>> and a compressor's "hash=HASH".
var bodyHashRe = regexp.MustCompile(`(?:<<ccr:|hash=)([0-9a-f]{24})`)

func hashesIn(body string) []string {
	var out []string
	for _, m := range bodyHashRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// A nil Store is a documented-supported configuration (Options.Store's doc
// comment). Dispatching a JSON-array block through it must forward the body
// verbatim, not panic inside the router's offload transforms.
func TestDispatchNilStoreForwardsVerbatim(t *testing.T) {
	body := userBodyWithText(t, repetitiveJSONBlock())

	res := Dispatch(body, Options{Router: router.NewDefault(), FrozenCount: 0})
	if res.Applied {
		t.Errorf("Applied = true, want a passthrough with no Store wired")
	}
	if string(res.Body) != string(body) {
		t.Error("Body must equal the input verbatim when nothing was rewritten")
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
