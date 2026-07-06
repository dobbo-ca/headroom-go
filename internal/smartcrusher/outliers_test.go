package smartcrusher

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// mustDecodeObjects decodes a JSON array literal into the []*orderedmap.OrderedMap
// form the outlier detectors expect. A JSON null element becomes a nil pointer
// (mirroring a "non-object" item that counts toward n but contributes no fields).
func mustDecodeObjects(t *testing.T, s string) []*orderedmap.OrderedMap {
	t.Helper()
	v, err := decodeJSON(s)
	if err != nil {
		t.Fatalf("decodeJSON(%q) error: %v", s, err)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("decodeJSON(%q) = %T, want []any", s, v)
	}
	out := make([]*orderedmap.OrderedMap, len(arr))
	for i, e := range arr {
		if e == nil {
			out[i] = nil
			continue
		}
		om, ok := e.(*orderedmap.OrderedMap)
		if !ok {
			t.Fatalf("element %d = %T, want *orderedmap.OrderedMap or null", i, e)
		}
		out[i] = om
	}
	return out
}

// obj builds a single ordered object from alternating key/value pairs, decoding
// each value from a JSON literal so json.Number/string/null typing matches the
// decodeJSON contract.
func obj(t *testing.T, kv ...string) *orderedmap.OrderedMap {
	t.Helper()
	if len(kv)%2 != 0 {
		t.Fatalf("obj: odd number of args %d", len(kv))
	}
	om := orderedmap.New()
	for i := 0; i < len(kv); i += 2 {
		val, err := decodeJSON(kv[i+1])
		if err != nil {
			t.Fatalf("obj: decodeJSON(%q) error: %v", kv[i+1], err)
		}
		om.Set(kv[i], val)
	}
	return om
}

// -- DetectStructuralOutliers --

func TestDetectStructuralOutliersTooFew(t *testing.T) {
	// len<5 -> empty before any field counting.
	items := mustDecodeObjects(t, `[{"a":1},{"a":1},{"a":1},{"a":1}]`)
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("too-few: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliersRareField(t *testing.T) {
	// 10 items; index 5 carries an extra "rare" field present in only 10% of
	// items (count 1 < n*0.2 = 2 -> rare). "common" is in all 10 (>= n*0.8 = 8).
	items := make([]*orderedmap.OrderedMap, 10)
	for i := range items {
		if i == 5 {
			items[i] = obj(t, "common", `1`, "rare", `2`)
		} else {
			items[i] = obj(t, "common", `1`)
		}
	}
	want := []int{5}
	if got := DetectStructuralOutliers(items); !reflect.DeepEqual(got, want) {
		t.Errorf("rare-field: want %v, got %v", want, got)
	}
}

func TestDetectStructuralOutliers95OkPlus5Mixed(t *testing.T) {
	// 100 items: 95 status "ok", 5 with distinct status values. status is common
	// (count 100 >= 80). Cardinality 6 (ok + 5 distinct). Pareto: ok=95 covers
	// >=80% alone -> top_k={ok}; the 5 non-ok items are flagged.
	items := make([]*orderedmap.OrderedMap, 100)
	for i := 0; i < 95; i++ {
		items[i] = obj(t, "status", `"ok"`)
	}
	for i := 95; i < 100; i++ {
		items[i] = obj(t, "status", fmt.Sprintf(`"mixed-%d"`, i))
	}
	want := []int{95, 96, 97, 98, 99}
	if got := DetectStructuralOutliers(items); !reflect.DeepEqual(got, want) {
		t.Errorf("95ok+5mixed: want %v, got %v", want, got)
	}
}

func TestDetectStructuralOutliers60Info25Warn15Err(t *testing.T) {
	// 100 items: 60 INFO, 25 WARN, 15 distinct ERR values. Cardinality 17.
	// Pareto: INFO(60)+WARN(25)=85% >= 80 -> top_k={INFO,WARN}; the 15 ERR items
	// are flagged.
	items := make([]*orderedmap.OrderedMap, 100)
	for i := 0; i < 60; i++ {
		items[i] = obj(t, "status", `"INFO"`)
	}
	for i := 60; i < 85; i++ {
		items[i] = obj(t, "status", `"WARN"`)
	}
	for i := 85; i < 100; i++ {
		items[i] = obj(t, "status", fmt.Sprintf(`"ERR-%d"`, i))
	}
	want := make([]int, 0, 15)
	for i := 85; i < 100; i++ {
		want = append(want, i)
	}
	got := DetectStructuralOutliers(items)
	if len(got) != 15 || !reflect.DeepEqual(got, want) {
		t.Errorf("60INFO+25WARN+15ERR: want %v, got %v", want, got)
	}
}

func TestDetectStructuralOutliersUniform50Distinct(t *testing.T) {
	// 50 items each with a distinct status. Cardinality 50 (<=50 ok). Reaching
	// 80% (threshold 40) needs 40 distinct values -> top_k grows past 5 -> skip.
	items := make([]*orderedmap.OrderedMap, 50)
	for i := range items {
		items[i] = obj(t, "status", fmt.Sprintf(`"s-%d"`, i))
	}
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("uniform-50: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliersCardinality60(t *testing.T) {
	// 60 distinct status values -> cardinality 60 > 50 -> gate fails -> empty.
	items := make([]*orderedmap.OrderedMap, 60)
	for i := range items {
		items[i] = obj(t, "status", fmt.Sprintf(`"s-%d"`, i))
	}
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("cardinality-60: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliersCardinality1(t *testing.T) {
	// All identical status -> cardinality 1 < 2 -> gate fails -> empty.
	items := make([]*orderedmap.OrderedMap, 10)
	for i := range items {
		items[i] = obj(t, "status", `"ok"`)
	}
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("cardinality-1: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliers95OkPlus5Null(t *testing.T) {
	// 95 "ok" + 5 null. Nulls are EXCLUDED from cardinality -> distinct non-null
	// = {ok} = 1 -> gate short-circuits before value_counts -> empty.
	items := make([]*orderedmap.OrderedMap, 100)
	for i := 0; i < 95; i++ {
		items[i] = obj(t, "status", `"ok"`)
	}
	for i := 95; i < 100; i++ {
		items[i] = obj(t, "status", `null`)
	}
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("95ok+5null: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliers90Ok5Warn5Null(t *testing.T) {
	// 90 "ok" + 5 "warn" + 5 null. Non-null distinct = {ok,warn} = 2 -> gate
	// passes. Once cardinality>=2, nulls count as "__none__" in value_counts.
	// ok=90 covers >=80% alone -> top_k={ok}; the 5 warn AND 5 null items flagged.
	items := make([]*orderedmap.OrderedMap, 100)
	for i := 0; i < 90; i++ {
		items[i] = obj(t, "status", `"ok"`)
	}
	for i := 90; i < 95; i++ {
		items[i] = obj(t, "status", `"warn"`)
	}
	for i := 95; i < 100; i++ {
		items[i] = obj(t, "status", `null`)
	}
	want := make([]int, 0, 10)
	for i := 90; i < 100; i++ {
		want = append(want, i)
	}
	got := DetectStructuralOutliers(items)
	if len(got) != 10 || !reflect.DeepEqual(got, want) {
		t.Errorf("90ok+5warn+5null: want %v, got %v", want, got)
	}
}

func TestDetectStructuralOutliersNonObjectCountsTowardN(t *testing.T) {
	// A nil (non-object) element counts toward n but contributes no fields and is
	// skipped in scans. 5 items: 4 objects + 1 null. n=5 (null included), so the
	// common threshold is 5*0.8=4.0 and "common" (in 4 objects) stays common; its
	// values are all identical so the rare-status gate fails. Nothing is flagged,
	// and the nil element must not panic.
	items := mustDecodeObjects(t, `[{"common":1},{"common":1},{"common":1},{"common":1},null]`)
	if got := DetectStructuralOutliers(items); len(got) != 0 {
		t.Errorf("non-object-counts-toward-n: want empty, got %v", got)
	}
}

func TestDetectStructuralOutliersParetoCeil99(t *testing.T) {
	// total=99 -> threshold=ceil(99*0.8)=ceil(79.2)=80. 80 "ok" + 19 distinct:
	// ok(80) hits the threshold exactly -> top_k={ok}; the 19 distinct flagged.
	items := make([]*orderedmap.OrderedMap, 99)
	for i := 0; i < 80; i++ {
		items[i] = obj(t, "status", `"ok"`)
	}
	for i := 80; i < 99; i++ {
		items[i] = obj(t, "status", fmt.Sprintf(`"d-%d"`, i))
	}
	got := DetectStructuralOutliers(items)
	if len(got) != 19 {
		t.Errorf("pareto-ceil-99: want 19 flagged, got %d (%v)", len(got), got)
	}
}

// -- DetectRareStatusValues (raw, append-ordered, dup-prone) --

func TestDetectRareStatusValuesRaw(t *testing.T) {
	// Direct call with an explicit commonFields set. 95 "ok" + 5 distinct on the
	// "status" common field -> the 5 non-ok indices appended in scan order.
	items := make([]*orderedmap.OrderedMap, 100)
	for i := 0; i < 95; i++ {
		items[i] = obj(t, "status", `"ok"`)
	}
	for i := 95; i < 100; i++ {
		items[i] = obj(t, "status", fmt.Sprintf(`"mixed-%d"`, i))
	}
	common := map[string]struct{}{"status": {}}
	want := []int{95, 96, 97, 98, 99}
	if got := DetectRareStatusValues(items, common); !reflect.DeepEqual(got, want) {
		t.Errorf("raw rare-status: want %v, got %v", want, got)
	}
}

func TestDetectRareStatusValuesEmptyCommon(t *testing.T) {
	// No common fields -> nothing to scan -> empty.
	items := make([]*orderedmap.OrderedMap, 10)
	for i := range items {
		items[i] = obj(t, "status", `"ok"`)
	}
	if got := DetectRareStatusValues(items, map[string]struct{}{}); len(got) != 0 {
		t.Errorf("empty-common: want empty, got %v", got)
	}
}

// -- DetectErrorItemsForPreservation --

func TestDetectErrorItemsBasic(t *testing.T) {
	// Indices 1 and 3 contain error keywords in their JSON.
	items := mustDecodeObjects(t, `[
		{"msg":"all good"},
		{"msg":"error while saving"},
		{"msg":"fine"},
		{"msg":"request Failed"},
		{"msg":"ok"}
	]`)
	want := []int{1, 3}
	if got := DetectErrorItemsForPreservation(items, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("error-basic: want %v, got %v", want, got)
	}
}

func TestDetectErrorItemsCaseInsensitive(t *testing.T) {
	// Uppercase keyword still matches (haystack lowercased).
	items := mustDecodeObjects(t, `[{"a":"nope"},{"a":"TIMEOUT reached"}]`)
	want := []int{1}
	if got := DetectErrorItemsForPreservation(items, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("case-insensitive: want %v, got %v", want, got)
	}
}

func TestDetectErrorItemsCachedDrivesHit(t *testing.T) {
	// The item's own JSON has no keyword, but the provided cache string does -> the
	// cache drives the hit (the cache is trusted verbatim, lowercased).
	items := mustDecodeObjects(t, `[{"a":"clean"},{"a":"clean"}]`)
	itemStrings := []string{`{"a":"clean"}`, `{"a":"panic in worker"}`}
	want := []int{1}
	if got := DetectErrorItemsForPreservation(items, itemStrings); !reflect.DeepEqual(got, want) {
		t.Errorf("cached-drives-hit: want %v, got %v", want, got)
	}
}

func TestDetectErrorItemsShortCacheFallback(t *testing.T) {
	// Cache is shorter than items; index 0 uses the (clean) cache, index 1 falls
	// back to fresh serialize of its own JSON, which DOES contain a keyword.
	items := mustDecodeObjects(t, `[{"a":"clean"},{"a":"exception thrown"}]`)
	itemStrings := []string{`{"a":"clean"}`}
	want := []int{1}
	if got := DetectErrorItemsForPreservation(items, itemStrings); !reflect.DeepEqual(got, want) {
		t.Errorf("short-cache-fallback: want %v, got %v", want, got)
	}
}

func TestDetectErrorItemsNonDictSkipped(t *testing.T) {
	// A nil (non-object) element is skipped BEFORE the cache lookup and never
	// flagged, even if a stray cache string would match.
	items := []*orderedmap.OrderedMap{
		obj(t, "a", `"clean"`),
		nil,
		obj(t, "a", `"crash detected"`),
	}
	itemStrings := []string{`{"a":"clean"}`, `error error error`, `{"a":"crash detected"}`}
	want := []int{2}
	if got := DetectErrorItemsForPreservation(items, itemStrings); !reflect.DeepEqual(got, want) {
		t.Errorf("non-dict-skipped: want %v, got %v", want, got)
	}
}

func TestDetectErrorItemsEmpty(t *testing.T) {
	// No error items -> empty (nil-safe).
	items := mustDecodeObjects(t, `[{"a":"ok"},{"a":"fine"}]`)
	if got := DetectErrorItemsForPreservation(items, nil); len(got) != 0 {
		t.Errorf("no-error-items: want empty, got %v", got)
	}
}
