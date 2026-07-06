package smartcrusher

import (
	"encoding/json"
	"strings"
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

// TestCompactWriteNoHTMLEscape pins the ensure_ascii=False / "ASCII NOT escaped"
// contract for '<', '>', '&' [ref: crusher.rs line 549/577]. The regression that
// motivated it: iancoleman/orderedmap's MarshalJSON HTML-escapes internally, so an
// outer json.Encoder with SetEscapeHTML(false) left those bytes escaped whenever
// they appeared inside a NESTED object string value (the common arrays-of-objects
// path) even though top-level scalars stayed literal.
func TestCompactWriteNoHTMLEscape(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"bare string html chars literal", `"x < y & z > w"`, `"x < y & z > w"`},
		{"top-level array element literal", `["a<b", "c&d", "e>f"]`, `["a<b","c&d","e>f"]`},
		{"nested object value literal", `[{"a": "x < y & z > w"}]`, `[{"a":"x < y & z > w"}]`},
		{"deeply nested object value literal", `{"o": {"k": "<a>&<b>"}}`, `{"o":{"k":"<a>&<b>"}}`},
		{"embedded ccr marker survives", `{"_ccr_dropped": "<<ccr:abc123 5_rows_offloaded>>"}`, `{"_ccr_dropped":"<<ccr:abc123 5_rows_offloaded>>"}`},
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

// TestCrush_ObjectStringHTMLCharsNotEscaped is the end-to-end guard: crushing an
// array of objects whose string values contain '<', '>', '&' must leave those
// bytes LITERAL in the raw Compressed output (no </>/& inflation).
func TestCrush_ObjectStringHTMLCharsNotEscaped(t *testing.T) {
	// Below MinItemsToAnalyze so the array is walked (objects recursed) but not
	// crushed — this isolates the object string-value serialization path.
	content := `[{"a":"x < y & z > w"},{"a":"p<q"}]`
	sc := NewSmartCrusher(DefaultConfig())
	res := sc.Crush(content, "", 0.0)

	for _, ch := range []string{"<", ">", "&"} {
		if !strings.Contains(res.Compressed, ch) {
			t.Errorf("Compressed missing literal %q: %q", ch, res.Compressed)
		}
	}
	// The Go/Python HTML escapes for < > & are the six-byte < / > /
	// & sequences. Correct output for this input contains no backslash escapes
	// at all, so ANY backslash means something was escaped.
	if strings.ContainsRune(res.Compressed, '\\') {
		t.Errorf("Compressed contains a backslash escape (chars should be literal): %q", res.Compressed)
	}
	// The full value must round-trip byte-for-byte as the un-escaped form.
	if !strings.Contains(res.Compressed, `"x < y & z > w"`) {
		t.Errorf("Compressed missing literal object value: %q", res.Compressed)
	}
}

// TestCrush_LossyMarkerSurvivesVerbatimInRawOutput is the end-to-end guard for the
// CCR sentinel: the lossy "<<ccr:HASH N_rows_offloaded>>" marker must appear
// VERBATIM in the raw Compressed string (previously the '<'/'>' in the marker were
// HTML-escaped, so no production consumer scanning the raw text could match it).
func TestCrush_LossyMarkerSurvivesVerbatimInRawOutput(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99 // force the lossy drop path.
	sc := NewSmartCrusher(cfg)

	res := sc.Crush(buildErrorDicts(30), "", 0.0)
	if !res.WasModified {
		t.Fatalf("lossy drop should mark modified")
	}
	// Recover the exact marker the crusher emitted (hash + dropped count) so the
	// raw-string assertion is against the real sentinel, not a guessed shape.
	items, err := decodeArrayTest(buildErrorDicts(30))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := sc.crushArray(items, "", 0.0)
	if r.DroppedSummary == "" {
		t.Fatalf("expected a dropped-row summary marker")
	}
	if !strings.HasPrefix(r.DroppedSummary, "<<ccr:") || !strings.HasSuffix(r.DroppedSummary, "_rows_offloaded>>") {
		t.Fatalf("marker shape unexpected: %q", r.DroppedSummary)
	}
	// The whole marker (including its '<'/'>') must survive verbatim in the RAW text.
	if !strings.Contains(res.Compressed, r.DroppedSummary) {
		t.Errorf("raw Compressed missing verbatim marker %q\noutput: %q", r.DroppedSummary, res.Compressed)
	}
	// The literal '<' / '>' of the marker must be present, and nothing in this input
	// needs a backslash escape, so any backslash means the marker was HTML-escaped.
	if !strings.Contains(res.Compressed, "<<ccr:") {
		t.Errorf("marker '<<ccr:' prefix not literal in raw output: %q", res.Compressed)
	}
	if strings.ContainsRune(res.Compressed, '\\') {
		t.Errorf("marker angle brackets HTML-escaped in raw output: %q", res.Compressed)
	}
}
