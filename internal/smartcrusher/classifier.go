package smartcrusher

import (
	"encoding/json"

	"github.com/iancoleman/orderedmap"
)

// ArrayType is the coarse element-homogeneity label ClassifyArray assigns to a
// JSON array. It is a pure value classifier — no CCR markers, no sampling. Only
// DictArray drives an MVP strategy (the lossless compaction-table path); the
// other labels' downstream crushers are DEFERRED (Plan 4+), but every label is
// emitted so routing sees the true shape.
type ArrayType int

const (
	DictArray ArrayType = iota
	StringArray
	NumberArray
	BoolArray
	NestedArray
	MixedArray
	Empty
)

// AsStr returns the parity-pinned lowercase identifier for the type. These
// literals are byte-pinned (Decision B exception) — do NOT relax them.
func (t ArrayType) AsStr() string {
	switch t {
	case DictArray:
		return "dict_array"
	case StringArray:
		return "string_array"
	case NumberArray:
		return "number_array"
	case BoolArray:
		return "bool_array"
	case NestedArray:
		return "nested_array"
	case MixedArray:
		return "mixed_array"
	case Empty:
		return "empty"
	default:
		return "mixed_array"
	}
}

// ClassifyArray labels an array by its element types. It walks EVERY element (no
// sampling, no early break) and sets six presence flags, then applies the
// homogeneity decision. A single non-conforming element (including one null
// among objects, or a bool mixed with numbers) collapses the array to
// MixedArray. There is no pure-null gate, so [null, null] -> MixedArray.
//
// Elements are expected in the shapes decodeJSON produces: *orderedmap.OrderedMap
// for objects, []any for arrays, json.Number for numbers, and string/bool/nil for
// the remaining scalars. The Go type switch separates bool from number for us
// (like Rust serde), so [true, false, 1] falls through to MixedArray without any
// special-casing.
func ClassifyArray(items []any) ArrayType {
	// STEP1: empty gate is the ONLY early return.
	if len(items) == 0 {
		return Empty
	}

	// STEP2: single scan; set exactly one flag per element.
	var hasBool, hasNumber, hasString, hasObject, hasArray, hasNull bool
	for _, item := range items {
		switch item.(type) {
		case *orderedmap.OrderedMap:
			hasObject = true
		case []any:
			hasArray = true
		case string:
			hasString = true
		case bool:
			hasBool = true
		case json.Number, float64, int:
			hasNumber = true
		case nil:
			hasNull = true
		}
	}

	// STEP3-8: each homogeneous label requires its flag AND none of the others;
	// anything else (heterogeneous mix, or any null) falls through to MixedArray.
	switch {
	case hasBool && !hasNumber && !hasString && !hasObject && !hasArray && !hasNull:
		return BoolArray
	case hasObject && !hasBool && !hasNumber && !hasString && !hasArray && !hasNull:
		return DictArray
	case hasString && !hasBool && !hasNumber && !hasObject && !hasArray && !hasNull:
		return StringArray
	case hasNumber && !hasBool && !hasString && !hasObject && !hasArray && !hasNull:
		return NumberArray
	case hasArray && !hasBool && !hasNumber && !hasString && !hasObject && !hasNull:
		return NestedArray
	default:
		return MixedArray
	}
}
