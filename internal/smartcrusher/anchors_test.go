package smartcrusher

import (
	"testing"

	"github.com/iancoleman/orderedmap"
)

// hasAnchor reports whether the anchor set contains a. Anchor sets are
// map[string]struct{} because order is irrelevant query-side.
func hasAnchor(anchors map[string]struct{}, a string) bool {
	_, ok := anchors[a]
	return ok
}

// TestExtractQueryAnchors pins the five-regex + blocklist + trim + lowercasing
// extraction rules [ref: anchors.rs algorithm + edgeCases].
func TestExtractQueryAnchors(t *testing.T) {
	t.Run("empty text yields empty set", func(t *testing.T) {
		if got := ExtractQueryAnchors(""); len(got) != 0 {
			t.Fatalf("want empty set, got %v", got)
		}
	})

	t.Run("3-digit not an anchor, 4+ digits are", func(t *testing.T) {
		anchors := ExtractQueryAnchors("id 123 and 4567 and 89012")
		if hasAnchor(anchors, "123") {
			t.Errorf("3-digit 123 must NOT be an anchor")
		}
		if !hasAnchor(anchors, "4567") {
			t.Errorf("4-digit 4567 must be an anchor")
		}
		if !hasAnchor(anchors, "89012") {
			t.Errorf("5-digit 89012 must be an anchor")
		}
	})

	t.Run("UUID lowercased", func(t *testing.T) {
		anchors := ExtractQueryAnchors("see 550E8400-E29B-41D4-A716-446655440000 now")
		if !hasAnchor(anchors, "550e8400-e29b-41d4-a716-446655440000") {
			t.Errorf("UUID must be stored lowercased, got %v", anchors)
		}
	})

	t.Run("quoted <2 trimmed chars skipped; stored anchor is raw untrimmed inner lowercased", func(t *testing.T) {
		// Inner "  X  " trims to "X" (1 byte) -> skipped.
		anchors := ExtractQueryAnchors(`find "  X  "`)
		if len(anchors) != 0 {
			t.Errorf("single-char trimmed quoted string must be skipped, got %v", anchors)
		}
		// Inner "  Hi  " trims to "Hi" (2 bytes) -> kept, stored UNTRIMMED lowercased.
		anchors2 := ExtractQueryAnchors(`say "  Hi  "`)
		if !hasAnchor(anchors2, "  hi  ") {
			t.Errorf("stored anchor must be raw untrimmed inner lowercased %q, got %v", "  hi  ", anchors2)
		}
	})

	t.Run("hostname blocklist exact-equality", func(t *testing.T) {
		// "e.g" matches the hostname pattern but is blocklisted -> dropped.
		anchors := ExtractQueryAnchors("for e.g test")
		if hasAnchor(anchors, "e.g") {
			t.Errorf("blocklisted hostname e.g must be dropped, got %v", anchors)
		}
		// "e.g.com" is NOT exactly a blocklist entry -> kept (lowercased).
		anchors2 := ExtractQueryAnchors("visit E.G.com now")
		if !hasAnchor(anchors2, "e.g.com") {
			t.Errorf("non-blocklisted hostname e.g.com must be kept lowercased, got %v", anchors2)
		}
	})

	t.Run("hostname kept and lowercased", func(t *testing.T) {
		anchors := ExtractQueryAnchors("ping Example.COM please")
		if !hasAnchor(anchors, "example.com") {
			t.Errorf("hostname must be kept lowercased, got %v", anchors)
		}
	})

	t.Run("email kept and lowercased", func(t *testing.T) {
		anchors := ExtractQueryAnchors("mail Bob@Example.COM done")
		if !hasAnchor(anchors, "bob@example.com") {
			t.Errorf("email must be kept lowercased, got %v", anchors)
		}
	})
}

// TestItemMatchesAnchors pins the matching contract [ref: anchors.rs item_matches].
func TestItemMatchesAnchors(t *testing.T) {
	mustObj := func(s string) *orderedmap.OrderedMap {
		v, err := decodeJSON(s)
		if err != nil {
			t.Fatalf("decodeJSON(%q) error: %v", s, err)
		}
		om, ok := v.(*orderedmap.OrderedMap)
		if !ok {
			t.Fatalf("decodeJSON(%q) = %T, want *orderedmap.OrderedMap", s, v)
		}
		return om
	}

	t.Run("empty anchors -> false", func(t *testing.T) {
		item := mustObj(`{"a":1}`)
		if ItemMatchesAnchors(item, map[string]struct{}{}) {
			t.Errorf("empty anchors must never match")
		}
	})

	t.Run("value substring match", func(t *testing.T) {
		item := mustObj(`{"host":"example.com","n":4567}`)
		anchors := map[string]struct{}{"example.com": {}}
		if !ItemMatchesAnchors(item, anchors) {
			t.Errorf("anchor must match a value substring")
		}
	})

	t.Run("key substring match (pythonRepr includes single-quoted keys)", func(t *testing.T) {
		item := mustObj(`{"session_id":"x"}`)
		anchors := map[string]struct{}{"session_id": {}}
		if !ItemMatchesAnchors(item, anchors) {
			t.Errorf("anchor must match against object keys too")
		}
	})

	t.Run("null repr: 'none' matches {'val': None} but 'null' does not", func(t *testing.T) {
		item := mustObj(`{"val":null}`)
		if !ItemMatchesAnchors(item, map[string]struct{}{"none": {}}) {
			t.Errorf("anchor 'none' must match pythonRepr null form")
		}
		if ItemMatchesAnchors(item, map[string]struct{}{"null": {}}) {
			t.Errorf("anchor 'null' must NOT match pythonRepr null form (whole reason pythonRepr exists)")
		}
	})
}

// TestPythonRepr pins the Python-str stringifier used for anchor matching
// [ref: anchors.rs python_repr]. Strings ALWAYS single-quoted; objects use
// single-quoted keys in insertion order; None/True/False; numbers natural.
func TestPythonRepr(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"null", `null`, "None"},
		{"true", `true`, "True"},
		{"false", `false`, "False"},
		{"int", `42`, "42"},
		{"float", `1.5`, "1.5"},
		{"string always single-quoted", `"hi"`, "'hi'"},
		{"array joined by comma-space", `[1, 2, 3]`, "[1, 2, 3]"},
		{"array of strings single-quoted", `["a", "b"]`, "['a', 'b']"},
		{"object single-quoted keys insertion order", `{"b":1,"a":2}`, "{'b': 1, 'a': 2}"},
		{"object with null value", `{"val":null}`, "{'val': None}"},
		{"nested object and array", `{"k":[true,{"x":"y"}]}`, "{'k': [True, {'x': 'y'}]}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := decodeJSON(c.json)
			if err != nil {
				t.Fatalf("decodeJSON(%q) error: %v", c.json, err)
			}
			if got := pythonRepr(v); got != c.want {
				t.Errorf("pythonRepr(%s) = %q, want %q", c.json, got, c.want)
			}
		})
	}

	t.Run("always-single-quote drift reproduced", func(t *testing.T) {
		v, err := decodeJSON(`{"k":"it's fine"}`)
		if err != nil {
			t.Fatalf("decodeJSON error: %v", err)
		}
		// Python would switch to double quotes here; we reproduce always-single.
		if got := pythonRepr(v); got != "{'k': 'it's fine'}" {
			t.Errorf("pythonRepr drift = %q, want %q", got, "{'k': 'it's fine'}")
		}
	})
}
