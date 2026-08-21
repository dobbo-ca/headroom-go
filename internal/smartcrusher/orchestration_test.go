package smartcrusher

import (
	"reflect"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// decodeArrayObjs decodes a JSON array-of-objects into []*orderedmap.OrderedMap,
// asserting every element is an object. Test helper only.
func decodeArrayObjs(t *testing.T, s string) []*orderedmap.OrderedMap {
	t.Helper()
	v, err := decodeJSON(s)
	if err != nil {
		t.Fatalf("decodeJSON(%q) error: %v", s, err)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("decodeJSON(%q) = %T, want []any", s, v)
	}
	out := make([]*orderedmap.OrderedMap, 0, len(arr))
	for i, e := range arr {
		om, ok := e.(*orderedmap.OrderedMap)
		if !ok {
			t.Fatalf("element %d = %T, want *orderedmap.OrderedMap", i, e)
		}
		out = append(out, om)
	}
	return out
}

func sortedSetToSlice(set map[int]struct{}) []int {
	return sortedIntSet(set)
}

func TestDeduplicateIndicesByContent_LowestIndexWins(t *testing.T) {
	items := decodeArrayObjs(t, `[{"a":1},{"a":1},{"a":2},{"a":1}]`)
	in := map[int]struct{}{0: {}, 1: {}, 2: {}, 3: {}}
	got := DeduplicateIndicesByContent(in, toAnySlice(items))
	want := []int{0, 2}
	if !reflect.DeepEqual(sortedSetToSlice(got), want) {
		t.Fatalf("dedup = %v, want %v", sortedSetToSlice(got), want)
	}
}

func TestDeduplicateIndicesByContent_SkipsOutOfBounds(t *testing.T) {
	items := decodeArrayObjs(t, `[{"a":1},{"a":2}]`)
	in := map[int]struct{}{0: {}, 1: {}, 5: {}, 9: {}}
	got := DeduplicateIndicesByContent(in, toAnySlice(items))
	want := []int{0, 1}
	if !reflect.DeepEqual(sortedSetToSlice(got), want) {
		t.Fatalf("dedup = %v, want %v", sortedSetToSlice(got), want)
	}
}

func TestDeduplicateIndicesByContent_EmptyReturnsEmpty(t *testing.T) {
	items := decodeArrayObjs(t, `[{"a":1}]`)
	got := DeduplicateIndicesByContent(map[int]struct{}{}, toAnySlice(items))
	if len(got) != 0 {
		t.Fatalf("dedup(empty) = %v, want empty", sortedSetToSlice(got))
	}
}

func TestDeduplicateIndicesByContent_KeyOrderIndependent(t *testing.T) {
	// {b,a} and {a,b} must hash identically (sort_keys=true), so index 1 is a dup.
	items := decodeArrayObjs(t, `[{"b":1,"a":2},{"a":2,"b":1}]`)
	in := map[int]struct{}{0: {}, 1: {}}
	got := DeduplicateIndicesByContent(in, toAnySlice(items))
	want := []int{0}
	if !reflect.DeepEqual(sortedSetToSlice(got), want) {
		t.Fatalf("dedup = %v, want %v (key order must be independent)", sortedSetToSlice(got), want)
	}
}

func TestFillRemainingSlots_AtOrOverBudgetReturnsUnchanged(t *testing.T) {
	items := decodeArrayObjs(t, `[{"a":1},{"a":2},{"a":3},{"a":4}]`)
	in := map[int]struct{}{0: {}, 1: {}, 2: {}}
	got := FillRemainingSlots(in, toAnySlice(items), len(items), 3)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(sortedSetToSlice(got), want) {
		t.Fatalf("fill(at budget) = %v, want %v", sortedSetToSlice(got), want)
	}
	// Over budget: effectiveMax < len(keep).
	got2 := FillRemainingSlots(in, toAnySlice(items), len(items), 2)
	if !reflect.DeepEqual(sortedSetToSlice(got2), want) {
		t.Fatalf("fill(over budget) = %v, want %v", sortedSetToSlice(got2), want)
	}
}

func TestFillRemainingSlots_AddsDiverseUniquesUpToMax(t *testing.T) {
	items := decodeArrayObjs(t, `[{"a":1},{"a":2},{"a":3},{"a":4},{"a":5},{"a":6}]`)
	in := map[int]struct{}{0: {}}
	got := FillRemainingSlots(in, toAnySlice(items), len(items), 3)
	if len(got) != 3 {
		t.Fatalf("fill added to len %d, want 3", len(got))
	}
	if _, ok := got[0]; !ok {
		t.Fatalf("fill dropped seed index 0: %v", sortedSetToSlice(got))
	}
}

func TestFillRemainingSlots_SkipsContentDuplicates(t *testing.T) {
	// Indices 1,2,3 are all content-dups of 0; only distinct payloads may fill.
	items := decodeArrayObjs(t, `[{"a":1},{"a":1},{"a":1},{"a":1},{"a":2},{"a":3}]`)
	in := map[int]struct{}{0: {}}
	got := FillRemainingSlots(in, toAnySlice(items), len(items), 4)
	// Only 0 (seed) plus the two distinct payloads (indices 4,5) can be added.
	for idx := range got {
		if idx == 1 || idx == 2 || idx == 3 {
			t.Fatalf("fill included content-duplicate index %d: %v", idx, sortedSetToSlice(got))
		}
	}
	if len(got) != 3 {
		t.Fatalf("fill = %v (len %d), want 3 distinct payloads", sortedSetToSlice(got), len(got))
	}
}

func TestPrioritizeIndices_UnderBudgetPassthroughAfterDedup(t *testing.T) {
	cfg := DefaultConfig()
	items := decodeArrayObjs(t, `[{"a":1},{"a":1},{"a":2}]`)
	in := map[int]struct{}{0: {}, 1: {}, 2: {}}
	got := PrioritizeIndices(&cfg, in, toAnySlice(items), len(items), nil, 15)
	// dedup collapses 1 into 0 -> {0,2}; under budget -> return early.
	want := []int{0, 2}
	if !reflect.DeepEqual(sortedSetToSlice(got), want) {
		t.Fatalf("prioritize = %v, want %v", sortedSetToSlice(got), want)
	}
}

func TestPrioritizeIndices_KeepsErrorItemsWhenOverBudget(t *testing.T) {
	cfg := DefaultConfig()
	// 30 benign dicts plus a FATAL error row at index 20; budget 5 forces over-budget.
	items := make([]*orderedmap.OrderedMap, 0, 30)
	for i := 0; i < 30; i++ {
		om := orderedmap.New()
		if i == 20 {
			om.Set("msg", "FATAL: out of memory")
		} else {
			om.Set("msg", "ok")
			om.Set("i", float64(i)) // make each distinct so dedup keeps them
		}
		items = append(items, om)
	}
	// Rebuild distinct via decodeJSON path to keep json.Number typing.
	in := map[int]struct{}{}
	for i := 0; i < 30; i++ {
		in[i] = struct{}{}
	}
	got := PrioritizeIndices(&cfg, in, toAnySlice(items), 30, nil, 5)
	if _, ok := got[20]; !ok {
		t.Fatalf("prioritize dropped the FATAL error row (index 20): %v", sortedSetToSlice(got))
	}
}

func TestPrioritizeIndices_IncludesFirst3AndLast2WhenRoom(t *testing.T) {
	cfg := DefaultConfig()
	items := make([]*orderedmap.OrderedMap, 0, 30)
	for i := 0; i < 30; i++ {
		om := orderedmap.New()
		om.Set("i", float64(i))
		items = append(items, om)
	}
	in := map[int]struct{}{}
	for i := 0; i < 30; i++ {
		in[i] = struct{}{}
	}
	got := PrioritizeIndices(&cfg, in, toAnySlice(items), 30, nil, 10)
	for _, want := range []int{0, 1, 2, 28, 29} {
		if _, ok := got[want]; !ok {
			t.Fatalf("prioritize missing anchor index %d: %v", want, sortedSetToSlice(got))
		}
	}
}

func TestItemContentHash_ScalarAndObject(t *testing.T) {
	// Object with reordered keys hashes identically.
	obj1, _ := decodeJSON(`{"b":1,"a":2}`)
	obj2, _ := decodeJSON(`{"a":2,"b":1}`)
	if itemContentHash(obj1, 0) != itemContentHash(obj2, 0) {
		t.Fatalf("object hash not key-order independent")
	}
	// Hash width is 16 hex chars.
	if got := len(itemContentHash(obj1, 0)); got != 16 {
		t.Fatalf("hash width = %d, want 16", got)
	}
	// Null scalar uses the literal "None".
	nullHash := itemContentHash(nil, 3)
	if len(nullHash) != 16 {
		t.Fatalf("null hash width = %d, want 16", len(nullHash))
	}
}

func TestNumericAnomalyIndices_FlagsOutliers(t *testing.T) {
	cfg := DefaultConfig()
	// Nine values at 10 and one outlier at 200 (index 9); mean=29, variance=3610
	// (sample n-1), std=60.083, threshold=120.17; outlier deviates 171 > threshold.
	items := decodeArrayObjs(t, `[{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":200}]`)
	mean := 29.0
	variance := 3610.0
	fs := orderedmap.New()
	fs.Set("v", FieldStats{
		Name:      "v",
		FieldType: "numeric",
		MeanVal:   &mean,
		Variance:  &variance,
	})
	analysis := &ArrayAnalysis{FieldStats: fs}
	got := numericAnomalyIndices(&cfg, toAnySlice(items), analysis)
	if _, ok := got[9]; !ok {
		t.Fatalf("numericAnomalyIndices missing outlier index 9: %v", sortedSetToSlice(got))
	}
}

func TestNumericAnomalyIndices_NilAnalysisAndVarianceGuards(t *testing.T) {
	cfg := DefaultConfig()
	items := decodeArrayObjs(t, `[{"v":1},{"v":2}]`)
	if got := numericAnomalyIndices(&cfg, toAnySlice(items), nil); len(got) != 0 {
		t.Fatalf("nil analysis -> %v, want empty", sortedSetToSlice(got))
	}
	// variance <= 0 short-circuits.
	zero := 0.0
	fs := orderedmap.New()
	fs.Set("v", FieldStats{Name: "v", FieldType: "numeric", MeanVal: &zero, Variance: &zero})
	if got := numericAnomalyIndices(&cfg, toAnySlice(items), &ArrayAnalysis{FieldStats: fs}); len(got) != 0 {
		t.Fatalf("variance<=0 -> %v, want empty", sortedSetToSlice(got))
	}
}
