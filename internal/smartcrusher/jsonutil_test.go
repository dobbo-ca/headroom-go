package smartcrusher

import (
	"encoding/json"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// TestDecodeJSON pins the shared ordered-decode helper: objects decode into
// *orderedmap.OrderedMap with keys in INSERTION order (not sorted), numbers are
// json.Number (UseNumber), and scalars/errors round-trip as expected.
func TestDecodeJSON(t *testing.T) {
	t.Run("array of objects preserves insertion order", func(t *testing.T) {
		v, err := decodeJSON(`[{"b":1,"a":2},{"b":3,"a":4}]`)
		if err != nil {
			t.Fatalf("decodeJSON returned error: %v", err)
		}
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("want []any, got %T", v)
		}
		if len(arr) != 2 {
			t.Fatalf("want len 2, got %d", len(arr))
		}
		om0, ok := arr[0].(*orderedmap.OrderedMap)
		if !ok {
			t.Fatalf("want *orderedmap.OrderedMap element, got %T", arr[0])
		}
		if got := om0.Keys(); len(got) != 2 || got[0] != "b" || got[1] != "a" {
			t.Fatalf("want insertion-order keys [b a], got %v", got)
		}
		bVal, _ := om0.Get("b")
		if num, ok := bVal.(json.Number); !ok || num.String() != "1" {
			t.Fatalf("want json.Number(\"1\") for key b, got %T %v", bVal, bVal)
		}
		om1, ok := arr[1].(*orderedmap.OrderedMap)
		if !ok {
			t.Fatalf("want *orderedmap.OrderedMap element, got %T", arr[1])
		}
		if got := om1.Keys(); len(got) != 2 || got[0] != "b" || got[1] != "a" {
			t.Fatalf("want insertion-order keys [b a] on element 1, got %v", got)
		}
	})

	t.Run("bare number is json.Number", func(t *testing.T) {
		v, err := decodeJSON("42")
		if err != nil {
			t.Fatalf("decodeJSON returned error: %v", err)
		}
		num, ok := v.(json.Number)
		if !ok || num.String() != "42" {
			t.Fatalf("want json.Number(\"42\"), got %T %v", v, v)
		}
	})

	t.Run("bare string", func(t *testing.T) {
		v, err := decodeJSON(`"x"`)
		if err != nil {
			t.Fatalf("decodeJSON returned error: %v", err)
		}
		if s, ok := v.(string); !ok || s != "x" {
			t.Fatalf("want string \"x\", got %T %v", v, v)
		}
	})

	t.Run("malformed input errors", func(t *testing.T) {
		if _, err := decodeJSON("{bad"); err == nil {
			t.Fatal("want error for malformed input, got nil")
		}
	})
}

// TestPythonSafeJSONDumps pins the compact serializer: `,`/`:` separators with
// NO spaces, ASCII (non-ASCII) NOT escaped, key insertion order preserved
// [ref: crusher.rs python_safe_json_dumps].
func TestPythonSafeJSONDumps(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"array no spaces", `[1, 2, 3]`, "[1,2,3]"},
		{"object compact separators", `{"a": 1, "b": 2}`, `{"a":1,"b":2}`},
		{"object key order preserved", `{"b": 1, "a": 2}`, `{"b":1,"a":2}`},
		{"non-ascii not escaped", `{"k": "café"}`, `{"k":"café"}`},
		{"nested compact", `{"x": [1, {"y": 2}]}`, `{"x":[1,{"y":2}]}`},
		{"bare string", `"hi"`, `"hi"`},
		{"bare int", `42`, "42"},
		{"null", `null`, "null"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := decodeJSON(c.json)
			if err != nil {
				t.Fatalf("decodeJSON(%q) error: %v", c.json, err)
			}
			if got := pythonSafeJSONDumps(v); got != c.want {
				t.Errorf("pythonSafeJSONDumps(%s) = %q, want %q", c.json, got, c.want)
			}
		})
	}
}

// TestCompactSerialize pins compactSerialize: it renders any decoded value via
// the same compact serializer used for non-string crusher results.
func TestCompactSerialize(t *testing.T) {
	v, err := decodeJSON(`[{"b": 1, "a": 2}]`)
	if err != nil {
		t.Fatalf("decodeJSON error: %v", err)
	}
	if got := compactSerialize(v); got != `[{"b":1,"a":2}]` {
		t.Errorf("compactSerialize = %q, want %q", got, `[{"b":1,"a":2}]`)
	}
}
