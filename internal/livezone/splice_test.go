package livezone

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyReplacementsSingle(t *testing.T) {
	orig := []byte(`{"a":"HELLO","b":"keep"}`)
	// Replace the value "HELLO" (with quotes) at its offset.
	start := bytes.Index(orig, []byte(`"HELLO"`))
	reps := []replacement{{start: start, end: start + len(`"HELLO"`), repl: []byte(`"HI"`)}}

	out, ranges := applyReplacements(orig, reps)
	if string(out) != `{"a":"HI","b":"keep"}` {
		t.Errorf("out = %q", out)
	}
	if len(ranges) != 1 {
		t.Fatalf("got %d ranges, want 1", len(ranges))
	}
	if ranges[0].Start != start || ranges[0].End != start+7 || ranges[0].NewLen != 4 {
		t.Errorf("range = %+v", ranges[0])
	}
}

// Replacements handed over out of order must still produce the right bytes
// and ascending ranges.
func TestApplyReplacementsSortsByStart(t *testing.T) {
	orig := []byte(`[ "AAA" , "BBB" , "CCC" ]`)
	a := bytes.Index(orig, []byte(`"AAA"`))
	b := bytes.Index(orig, []byte(`"BBB"`))
	c := bytes.Index(orig, []byte(`"CCC"`))

	reps := []replacement{
		{start: c, end: c + 5, repl: []byte(`"3"`)},
		{start: a, end: a + 5, repl: []byte(`"1"`)},
		{start: b, end: b + 5, repl: []byte(`"2"`)},
	}
	out, ranges := applyReplacements(orig, reps)
	if string(out) != `[ "1" , "2" , "3" ]` {
		t.Errorf("out = %q", out)
	}
	for i := 1; i < len(ranges); i++ {
		if ranges[i].Start < ranges[i-1].Start {
			t.Errorf("ranges are not ascending: %+v", ranges)
		}
	}
}

// The bytes between replacements must be copied, never regenerated. The
// awkward spacing here is what proves it.
func TestApplyReplacementsPreservesGapsVerbatim(t *testing.T) {
	orig := []byte("{\n\t\"a\" : \"AAA\" ,\n\t\"b\" : 1.50 ,\n\t\"c\" : \"CCC\"\n}")
	a := bytes.Index(orig, []byte(`"AAA"`))
	c := bytes.Index(orig, []byte(`"CCC"`))

	reps := []replacement{
		{start: a, end: a + 5, repl: []byte(`"x"`)},
		{start: c, end: c + 5, repl: []byte(`"y"`)},
	}
	out, _ := applyReplacements(orig, reps)
	want := "{\n\t\"a\" : \"x\" ,\n\t\"b\" : 1.50 ,\n\t\"c\" : \"y\"\n}"
	if string(out) != want {
		t.Errorf("out  = %q\nwant = %q", out, want)
	}
}

func TestApplyReplacementsEmpty(t *testing.T) {
	orig := []byte(`{"a":1}`)
	out, ranges := applyReplacements(orig, nil)
	if string(out) != string(orig) {
		t.Errorf("out = %q, want the original", out)
	}
	if len(ranges) != 0 {
		t.Errorf("got %d ranges, want 0", len(ranges))
	}
}

// A replacement that overlaps an earlier one must be dropped, not applied at
// a shifted offset.
func TestApplyReplacementsDropsOverlaps(t *testing.T) {
	orig := []byte(`0123456789`)
	reps := []replacement{
		{start: 2, end: 6, repl: []byte(`X`)},
		{start: 4, end: 8, repl: []byte(`Y`)},
	}
	out, ranges := applyReplacements(orig, reps)
	if string(out) != `01X6789` {
		t.Errorf("out = %q, want %q", out, `01X6789`)
	}
	if len(ranges) != 1 {
		t.Errorf("got %d ranges, want 1 (the overlap must be dropped)", len(ranges))
	}
}

func TestApplyReplacementsAtBoundaries(t *testing.T) {
	orig := []byte(`ABCDEF`)
	out, _ := applyReplacements(orig, []replacement{{start: 0, end: 2, repl: []byte(`z`)}})
	if string(out) != `zCDEF` {
		t.Errorf("leading replacement: out = %q", out)
	}
	out, _ = applyReplacements(orig, []replacement{{start: 4, end: 6, repl: []byte(`z`)}})
	if string(out) != `ABCDz` {
		t.Errorf("trailing replacement: out = %q", out)
	}
}

// The replacement must be a valid JSON string literal that round-trips.
func TestEncodeJSONString(t *testing.T) {
	inputs := []string{
		"plain",
		`with "quotes"`,
		`with \ backslash`,
		"with\nnewline\ttab",
		"café — unicode ✓",
		"",
		"a < b && c > d",
		"\x00\x01 control chars",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			enc := encodeJSONString(in)
			if len(enc) < 2 || enc[0] != '"' || enc[len(enc)-1] != '"' {
				t.Fatalf("encodeJSONString(%q) = %q, want an enclosed JSON string", in, enc)
			}
			if bytes.HasSuffix(enc, []byte("\n")) {
				t.Fatalf("encodeJSONString must not emit a trailing newline: %q", enc)
			}
			var back string
			if err := json.Unmarshal(enc, &back); err != nil {
				t.Fatalf("json.Unmarshal(%q) failed: %v", enc, err)
			}
			if back != in {
				t.Errorf("round-trip: got %q, want %q", back, in)
			}
		})
	}
}

// HTML escaping must be OFF: json.Marshal would turn < into <, which
// inflates the payload for no benefit.
func TestEncodeJSONStringDoesNotEscapeHTML(t *testing.T) {
	enc := string(encodeJSONString("a < b > c & d"))
	// Search for the ESCAPED form. The literal characters are always present
	// because they are in the input.
	for _, bad := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(enc, bad) {
			t.Errorf("encodeJSONString escaped HTML (%s): %q", bad, enc)
		}
	}
	if !strings.Contains(enc, "a < b > c & d") {
		t.Errorf("literal characters did not survive: %q", enc)
	}
}

// Determinism (I4): encoding the same string twice must give the same bytes.
func TestEncodeJSONStringIsDeterministic(t *testing.T) {
	s := "café \"x\" <y> \n tab\t"
	first := encodeJSONString(s)
	for i := 0; i < 50; i++ {
		if !bytes.Equal(encodeJSONString(s), first) {
			t.Fatal("encodeJSONString is not deterministic")
		}
	}
}
