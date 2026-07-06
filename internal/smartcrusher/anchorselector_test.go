package smartcrusher

import (
	"reflect"
	"sort"
	"testing"
)

// [ref: builder.rs (anchor_selector) + (d) SAMPLING] MVP SelectAnchors returns
// the union of the first min(3,n) indices, the last 2 indices, and every index
// whose item matches a query anchor (ItemMatchesAnchors). The result is an
// ascending, deduplicated, in-bounds index set.

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

func TestSelectAnchorsEmptyItems(t *testing.T) {
	sel := NewAnchorSelector(DefaultAnchorConfig())
	got := sel.SelectAnchors(nil, 15, AnchorGeneric, "")
	if len(got) != 0 {
		t.Fatalf("SelectAnchors(nil) = %v, want empty", got)
	}
}

func TestSelectAnchorsFirst3Last2NoQuery(t *testing.T) {
	// 8 items, no query => first min(3,8)={0,1,2} union last 2={6,7}.
	items := mustDecodeObjects(t, `[
		{"i":0},{"i":1},{"i":2},{"i":3},
		{"i":4},{"i":5},{"i":6},{"i":7}
	]`)
	sel := NewAnchorSelector(DefaultAnchorConfig())
	got := sel.SelectAnchors(items, 15, AnchorGeneric, "")
	want := []int{0, 1, 2, 6, 7}
	if !reflect.DeepEqual(sortedInts(got), want) {
		t.Fatalf("SelectAnchors = %v, want %v", got, want)
	}
}

func TestSelectAnchorsSmallArrayOverlap(t *testing.T) {
	// 2 items: first min(3,2)={0,1}, last 2={0,1} => union {0,1} (idempotent set).
	items := mustDecodeObjects(t, `[{"i":0},{"i":1}]`)
	sel := NewAnchorSelector(DefaultAnchorConfig())
	got := sel.SelectAnchors(items, 15, AnchorGeneric, "")
	if !reflect.DeepEqual(sortedInts(got), []int{0, 1}) {
		t.Fatalf("SelectAnchors(2 items) = %v, want [0 1]", got)
	}
}

func TestSelectAnchorsQueryUnion(t *testing.T) {
	// Middle item (index 4) carries a 4+ digit id anchor that appears in the
	// query; it must be pulled in on top of first-3/last-2.
	items := mustDecodeObjects(t, `[
		{"id":"a"},{"id":"b"},{"id":"c"},{"id":"d"},
		{"id":"12345"},{"id":"f"},{"id":"g"},{"id":"h"}
	]`)
	sel := NewAnchorSelector(DefaultAnchorConfig())
	got := sel.SelectAnchors(items, 15, AnchorGeneric, "trace 12345 please")
	// first {0,1,2} + last {6,7} + query match at 4.
	want := []int{0, 1, 2, 4, 6, 7}
	if !reflect.DeepEqual(sortedInts(got), want) {
		t.Fatalf("SelectAnchors query = %v, want %v", got, want)
	}
}

func TestSelectAnchorsInBoundsAndDeduped(t *testing.T) {
	// Query matches a first-anchor row too; result must stay deduped and in-bounds.
	items := mustDecodeObjects(t, `[
		{"id":"7777"},{"id":"b"},{"id":"c"},{"id":"d"},{"id":"e"}
	]`)
	sel := NewAnchorSelector(DefaultAnchorConfig())
	got := sel.SelectAnchors(items, 15, AnchorGeneric, "look at 7777")
	// first {0,1,2} + last {3,4} + query match {0} => {0,1,2,3,4}, no dup of 0.
	want := []int{0, 1, 2, 3, 4}
	if !reflect.DeepEqual(sortedInts(got), want) {
		t.Fatalf("SelectAnchors = %v, want %v", got, want)
	}
	for _, idx := range got {
		if idx < 0 || idx >= len(items) {
			t.Fatalf("out-of-bounds index %d (n=%d)", idx, len(items))
		}
	}
}

func TestSelectAnchorsIdempotent(t *testing.T) {
	items := mustDecodeObjects(t, `[{"i":0},{"i":1},{"i":2},{"i":3},{"i":4}]`)
	sel := NewAnchorSelector(DefaultAnchorConfig())
	a := sortedInts(sel.SelectAnchors(items, 15, AnchorGeneric, "abc"))
	b := sortedInts(sel.SelectAnchors(items, 15, AnchorGeneric, "abc"))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("SelectAnchors not idempotent: %v vs %v", a, b)
	}
}
