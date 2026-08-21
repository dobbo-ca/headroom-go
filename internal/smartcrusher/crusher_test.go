package smartcrusher

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/offloads"
)

// buildUniformDicts renders a JSON array of n identical-schema {id,level,msg}
// objects with distinct ids so the array is a clean lossless-table candidate.
func buildUniformDicts(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"level":"info","msg":"ok"}`, i)
	}
	b.WriteByte(']')
	return b.String()
}

// buildErrorDicts renders a JSON array of n objects. Every 3rd carries an
// "error" level and a changepoint-bearing numeric "code"; the rest are "info".
// The array is uniform-schema so the planner (not a head-slice) chooses kept rows.
func buildErrorDicts(n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		level := "info"
		msg := "request handled"
		if i%3 == 0 {
			level = "error"
			msg = "connection failed"
		}
		fmt.Fprintf(&b, `{"idx":%d,"level":%q,"msg":%q,"code":%d}`, i, level, msg, 200+i)
	}
	b.WriteByte(']')
	return b.String()
}

func TestNewSmartCrusher_DefaultHasWorkingStore(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	if sc.ccrStore == nil {
		t.Fatalf("default crusher ccrStore nil (Task-13 registration failed?)")
	}
	if got := sc.CcrLen(); got != 0 {
		t.Fatalf("fresh crusher CcrLen = %d, want 0", got)
	}
}

func TestNewSmartCrusherWithoutCompaction_NoCompactionStage(t *testing.T) {
	sc := NewSmartCrusherWithoutCompaction(DefaultConfig())
	if sc.compaction != nil {
		t.Fatalf("without-compaction crusher should have no compaction stage")
	}
	if sc.ccrStore == nil {
		t.Fatalf("without-compaction crusher still needs a lossy CCR store")
	}
}

func TestCrush_EmptyArray(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	crushed, mod, strategy := sc.smartCrushContent("[]", "", 0.0)
	if crushed != "[]" {
		t.Fatalf("empty array crushed = %q, want []", crushed)
	}
	if mod {
		t.Fatalf("empty array WasModified = true, want false")
	}
	// Empty is the only false-modified array case; strategy is passthrough at seam.
	if strategy != "" {
		t.Fatalf("empty array info = %q, want empty", strategy)
	}
	res := sc.Crush("[]", "", 0.0)
	if res.Compressed != "[]" || res.WasModified {
		t.Fatalf("Crush([]) = %+v, want {Compressed:[], WasModified:false}", res)
	}
}

func TestCrush_ShortArrayWhitespaceNormalizedIsModified(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	res := sc.Crush("[1, 2, 3]", "", 0.0)
	if res.Compressed != "[1,2,3]" {
		t.Fatalf("Crush([1, 2, 3]) compressed = %q, want [1,2,3]", res.Compressed)
	}
	if !res.WasModified {
		t.Fatalf("whitespace-normalized array should be WasModified=true")
	}
}

func TestCrush_AlreadyCompactShortArrayNotModified(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	res := sc.Crush("[1,2,3]", "", 0.0)
	if res.WasModified {
		t.Fatalf("already-compact array should not be modified")
	}
}

func TestCrush_NonJSONPassthrough(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	crushed, mod, info := sc.smartCrushContent("hello", "", 0.0)
	if crushed != "hello" || mod || info != "" {
		t.Fatalf("non-JSON = (%q,%v,%q), want (hello,false,\"\")", crushed, mod, info)
	}
	res := sc.Crush("hello", "", 0.0)
	if res.Compressed != "hello" || res.WasModified {
		t.Fatalf("Crush(hello) = %+v, want passthrough", res)
	}
}

func TestCrush_ScalarJSONRoundTrips(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	res := sc.Crush("42", "", 0.0)
	if res.Compressed != "42" || res.WasModified {
		t.Fatalf("Crush(42) = %+v, want unchanged", res)
	}
}

func TestCrush_LosslessTableWin(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	content := buildUniformDicts(50)
	crushed, mod, info := sc.smartCrushContent(content, "", 0.0)
	if !mod {
		t.Fatalf("uniform tabular array should be modified")
	}
	if len(crushed) >= len(content) {
		t.Fatalf("lossless table did not shrink: crushed=%d content=%d", len(crushed), len(content))
	}
	if !strings.HasPrefix(info, "lossless:table") {
		t.Fatalf("info = %q, want prefix lossless:table", info)
	}
	// Lossless: nothing dropped, no lossy CCR hash written.
	if sc.CcrLen() != 0 {
		t.Fatalf("lossless win wrote to CCR store (Len=%d), want 0", sc.CcrLen())
	}
}

func TestCrushArray_Tier1NonTruncation(t *testing.T) {
	// 30 uniform error/changepoint dicts, MaxItemsAfterCrush=15, lossy path forced.
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99 // force past the lossless gate.
	sc := NewSmartCrusher(cfg)

	items, err := decodeArrayTest(buildErrorDicts(30))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := sc.crushArray(items, "", 0.0)
	// Tier-1 must NOT fire (30 > 15) and must NOT head-slice items[0:15].
	if r.StrategyInfo == "none:adaptive_at_limit" {
		t.Fatalf("Tier-1 fired for n=30 > max=15; should proceed to lossy funnel")
	}
	if len(r.Items) >= 30 {
		t.Fatalf("lossy path kept %d rows, want a drop below 30", len(r.Items))
	}
	// The kept set is PLANNER-selected: at least one kept row is an error row at an
	// original index >= 15 (a head-slice items[0:15] could never keep such a row).
	keptBeyond15 := false
	for _, it := range r.Items {
		obj, ok := it.(interface{ Get(string) (any, bool) })
		if !ok {
			continue
		}
		idxVal, _ := obj.Get("idx")
		if n, ok := idxVal.(interface{ Int64() (int64, error) }); ok {
			if v, err := n.Int64(); err == nil && v >= 15 {
				keptBeyond15 = true
				break
			}
		}
	}
	if !keptBeyond15 {
		t.Fatalf("no kept row has original idx >= 15; looks like a head-slice truncation")
	}
}

func TestCrushArray_LossyDropWritesSpaceFormMarker(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99
	sc := NewSmartCrusher(cfg)

	items, err := decodeArrayTest(buildErrorDicts(30))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := sc.crushArray(items, "", 0.0)
	if r.CcrHash == nil {
		t.Fatalf("lossy drop produced no CcrHash")
	}
	dropped := 30 - len(r.Items)
	if dropped <= 0 {
		t.Fatalf("expected dropped rows, kept=%d", len(r.Items))
	}
	// Space-form marker: <<ccr:HASH N_rows_offloaded>>, 12-hex hash.
	wantMarker := fmt.Sprintf("<<ccr:%s %d_rows_offloaded>>", *r.CcrHash, dropped)
	if r.DroppedSummary != wantMarker {
		t.Fatalf("DroppedSummary = %q, want %q", r.DroppedSummary, wantMarker)
	}
	if len(*r.CcrHash) != 12 {
		t.Fatalf("space-form hash width = %d, want 12", len(*r.CcrHash))
	}
	// Round-trip: the store holds the canonical original slice, byte-exact.
	got, ok := sc.CcrGet(*r.CcrHash)
	if !ok {
		t.Fatalf("CcrGet(%q) miss", *r.CcrHash)
	}
	if sc.CcrLen() != 1 {
		t.Fatalf("CcrLen after lossy = %d, want 1", sc.CcrLen())
	}
	// Idempotent: re-crushing the same slice yields the same hash and no growth.
	items2, _ := decodeArrayTest(buildErrorDicts(30))
	r2 := sc.crushArray(items2, "", 0.0)
	if r2.CcrHash == nil || *r2.CcrHash != *r.CcrHash {
		t.Fatalf("re-crush hash changed: %v vs %v", r2.CcrHash, r.CcrHash)
	}
	if sc.CcrLen() != 1 {
		t.Fatalf("re-crush grew store to %d, want 1 (idempotent)", sc.CcrLen())
	}
	// The stored canonical payload must re-decode to the original 30 items.
	reparsed, err := decodeArrayTest(got)
	if err != nil {
		t.Fatalf("stored payload not valid JSON array: %v", err)
	}
	if len(reparsed) != 30 {
		t.Fatalf("stored payload decoded to %d items, want 30", len(reparsed))
	}
}

func TestCrushArray_EnableCCRMarkerFalseNoStoreWrite(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99
	cfg.EnableCCRMarker = false
	sc := NewSmartCrusher(cfg)

	items, _ := decodeArrayTest(buildErrorDicts(30))
	r := sc.crushArray(items, "", 0.0)
	if len(r.Items) >= 30 {
		t.Fatalf("rows should still drop with EnableCCRMarker=false, kept=%d", len(r.Items))
	}
	if r.CcrHash != nil {
		t.Fatalf("EnableCCRMarker=false should not set CcrHash, got %q", *r.CcrHash)
	}
	if r.DroppedSummary != "" {
		t.Fatalf("EnableCCRMarker=false should not set DroppedSummary, got %q", r.DroppedSummary)
	}
	if sc.CcrLen() != 0 {
		t.Fatalf("EnableCCRMarker=false wrote to store (Len=%d)", sc.CcrLen())
	}
}

func TestProcessValue_DropSentinelIsLastElement(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99
	sc := NewSmartCrusher(cfg)

	crushed, mod, _ := sc.smartCrushContent(buildErrorDicts(30), "", 0.0)
	if !mod {
		t.Fatalf("lossy drop should mark modified")
	}
	reparsed, err := decodeArrayTest(crushed)
	if err != nil {
		t.Fatalf("crushed output not a JSON array: %v (%q)", err, crushed)
	}
	last := reparsed[len(reparsed)-1]
	obj, ok := last.(interface{ Get(string) (any, bool) })
	if !ok {
		t.Fatalf("last element not an object: %T", last)
	}
	marker, present := obj.Get("_ccr_dropped")
	if !present {
		t.Fatalf("last element missing _ccr_dropped sentinel")
	}
	ms, _ := marker.(string)
	if !strings.HasPrefix(ms, "<<ccr:") || !strings.HasSuffix(ms, "_rows_offloaded>>") {
		t.Fatalf("_ccr_dropped marker malformed: %q", ms)
	}
}

func TestProcessValue_MaxDepthGuard(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	// 100 levels of nested single-key objects, then a crushable inner array. Past
	// MAX_DEPTH=50 the walker returns the value unchanged and must not panic.
	var sb strings.Builder
	depth := 100
	for i := 0; i < depth; i++ {
		sb.WriteString(`{"a":`)
	}
	sb.WriteString("1")
	for i := 0; i < depth; i++ {
		sb.WriteByte('}')
	}
	content := sb.String()
	crushed, _, _ := sc.smartCrushContent(content, "", 0.0)
	// No panic and output re-parses.
	if _, err := decodeJSON(crushed); err != nil {
		t.Fatalf("deeply nested output not valid JSON: %v", err)
	}
}

func TestCrush_SeamDropsStrategyAndOriginal(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	var _ offloads.Crusher = sc // compile-time: satisfies the seam.
	res := sc.Crush(buildUniformDicts(50), "", 0.0)
	// The seam type has exactly Compressed + WasModified (no Strategy/Original).
	_ = res.Compressed
	_ = res.WasModified
	if !res.WasModified {
		t.Fatalf("expected a modified result for a uniform tabular array")
	}
}

// decodeArrayTest decodes a JSON array string into []any (test helper reusing the
// package decoder).
func decodeArrayTest(s string) ([]any, error) {
	v, err := decodeJSON(s)
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("not a JSON array: %T", v)
	}
	return arr, nil
}
