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
