package smartcrusher

import "testing"

// mustDecode decodes a JSON literal into the []any form ClassifyArray expects,
// failing the test on a decode error. It exists so the table tests can express
// inputs as JSON source (matching the upstream fixtures) rather than by hand-
// building orderedmap values.
func mustDecode(t *testing.T, s string) []any {
	t.Helper()
	v, err := decodeJSON(s)
	if err != nil {
		t.Fatalf("decodeJSON(%q) error: %v", s, err)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("decodeJSON(%q) = %T, want []any", s, v)
	}
	return arr
}

func TestArrayTypeAsStr(t *testing.T) {
	// The 7 as_str literals are byte-pinned identifiers (Decision B exception).
	cases := []struct {
		typ  ArrayType
		want string
	}{
		{DictArray, "dict_array"},
		{StringArray, "string_array"},
		{NumberArray, "number_array"},
		{BoolArray, "bool_array"},
		{NestedArray, "nested_array"},
		{MixedArray, "mixed_array"},
		{Empty, "empty"},
	}
	for _, c := range cases {
		if got := c.typ.AsStr(); got != c.want {
			t.Errorf("ArrayType(%d).AsStr() = %q, want %q", c.typ, got, c.want)
		}
	}
}

func TestClassifyArray(t *testing.T) {
	cases := []struct {
		name string
		json string
		want ArrayType
	}{
		{"empty", `[]`, Empty},
		{"dict_array", `[{"a":1},{"b":2}]`, DictArray},
		{"null_in_array_is_mixed", `[{"a":1},null]`, MixedArray},
		{"string_array", `["a","b"]`, StringArray},
		{"number_array_int_and_float", `[1,2.5,3]`, NumberArray},
		{"bool_array", `[true,false]`, BoolArray},
		{"bool_and_number_is_mixed", `[true,false,1]`, MixedArray},
		{"nested_array", `[[1,2],[3,4]]`, NestedArray},
		{"dict_and_string_is_mixed", `[{"a":1},"s"]`, MixedArray},
		{"all_null_is_mixed", `[null,null]`, MixedArray},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := mustDecode(t, c.json)
			if got := ClassifyArray(items); got != c.want {
				t.Errorf("ClassifyArray(%s) = %s, want %s", c.json, got.AsStr(), c.want.AsStr())
			}
		})
	}
}
