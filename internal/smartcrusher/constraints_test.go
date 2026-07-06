package smartcrusher

import (
	"reflect"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// [ref: constraints.rs edgeCases] The two OSS constraints are stateless
// delegations to outliers.go; these tests pin names, ordering, empty-safety,
// and the itemStrings-cache contract (KeepErrors uses it, KeepStructuralOutliers
// ignores it, results identical either way).

func TestKeepErrorsConstraintName(t *testing.T) {
	if got := (KeepErrorsConstraint{}).Name(); got != "keep_errors" {
		t.Fatalf("Name() = %q, want %q", got, "keep_errors")
	}
}

func TestKeepStructuralOutliersConstraintName(t *testing.T) {
	if got := (KeepStructuralOutliersConstraint{}).Name(); got != "keep_structural_outliers" {
		t.Fatalf("Name() = %q, want %q", got, "keep_structural_outliers")
	}
}

func TestConstraintsHandleEmptyItems(t *testing.T) {
	empty := []*orderedmap.OrderedMap{}
	if got := (KeepErrorsConstraint{}).MustKeep(empty, nil); len(got) != 0 {
		t.Fatalf("KeepErrors.MustKeep(empty) = %v, want empty", got)
	}
	if got := (KeepStructuralOutliersConstraint{}).MustKeep(empty, nil); len(got) != 0 {
		t.Fatalf("KeepStructuralOutliers.MustKeep(empty) = %v, want empty", got)
	}
}

func TestDefaultOSSConstraintsOrderAndLength(t *testing.T) {
	cs := defaultOSSConstraints()
	if len(cs) != 2 {
		t.Fatalf("defaultOSSConstraints() length = %d, want 2", len(cs))
	}
	if cs[0].Name() != "keep_errors" {
		t.Fatalf("constraint[0].Name() = %q, want keep_errors", cs[0].Name())
	}
	if cs[1].Name() != "keep_structural_outliers" {
		t.Fatalf("constraint[1].Name() = %q, want keep_structural_outliers", cs[1].Name())
	}
}

// KeepErrors with a provided itemStrings cache must return the SAME indices as
// the nil path (the cache is a perf optimization only).
func TestKeepErrorsCacheIdenticalToNil(t *testing.T) {
	items := mustDecodeObjects(t, `[
		{"msg":"all good"},
		{"msg":"ERROR: boom"},
		{"msg":"fine"},
		{"msg":"fatal: crash"},
		{"msg":"ok"}
	]`)
	c := KeepErrorsConstraint{}

	fresh := c.MustKeep(items, nil)

	// Build the itemStrings cache from the same serialization the detector uses.
	cache := make([]string, len(items))
	for i, it := range items {
		cache[i] = stringifyOutlierValue(it)
	}
	cached := c.MustKeep(items, cache)

	if !reflect.DeepEqual(fresh, cached) {
		t.Fatalf("KeepErrors cached=%v != nil=%v", cached, fresh)
	}
	// Sanity: it actually flagged the two error rows.
	if !reflect.DeepEqual(fresh, []int{1, 3}) {
		t.Fatalf("KeepErrors indices = %v, want [1 3]", fresh)
	}
}

// KeepStructuralOutliers must IGNORE the itemStrings argument (upstream param is
// _item_strings): passing a bogus cache must not change the result.
func TestKeepStructuralOutliersIgnoresItemStrings(t *testing.T) {
	// 5 objects: four share {a}, one rare field {a,z} => index 4 is a structural
	// outlier via the rare field z.
	items := mustDecodeObjects(t, `[
		{"a":1},
		{"a":2},
		{"a":3},
		{"a":4},
		{"a":5,"z":9}
	]`)
	c := KeepStructuralOutliersConstraint{}

	withNil := c.MustKeep(items, nil)
	bogus := []string{"error", "error", "error", "error", "error"}
	withBogus := c.MustKeep(items, bogus)

	if !reflect.DeepEqual(withNil, withBogus) {
		t.Fatalf("KeepStructuralOutliers used itemStrings: nil=%v bogus=%v", withNil, withBogus)
	}
	if !reflect.DeepEqual(withNil, []int{4}) {
		t.Fatalf("KeepStructuralOutliers indices = %v, want [4]", withNil)
	}
}
