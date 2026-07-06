package smartcrusher

import (
	"encoding/json"
	"math"
	"testing"
)

// TestIsUUIDFormat pins the byte-length UUID recogniser [ref: statistics.rs
// edgeCases]. A valid 36-byte 8-4-4-4-12 hex string is accepted in both lower and
// UPPER case; anything off by a byte, with wrong segments, or with a non-hex char
// is rejected. Length is measured in BYTES (Rust .len()), NOT runes.
func TestIsUUIDFormat(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"123e4567-e89b-12d3-a456-426614174000", true},   // canonical lower
		{"123E4567-E89B-12D3-A456-426614174000", true},   // UPPER accepted
		{"123e4567-e89b-12d3-a456-42661417400", false},   // len 35
		{"123e4567-e89b-12d3-a456-4266141740000", false}, // len 37
		{"", false}, // empty
		{"123e4567e89b12d3a456426614174000abcd", false},  // 36 bytes, no dashes -> wrong segments
		{"123e4567-e89b-12d3-a456426614174000-x", false}, // wrong segment layout
		{"123e4567-e89b-12d3-a456-42661417400g", false},  // non-hex 'g'
		{"gggggggg-gggg-gggg-gggg-gggggggggggg", false},  // all non-hex
		{"123e4567-e89b-12d3-a456-4266141740Ω0", false},  // multibyte rune keeps byte len 36 but non-hex
	}
	for _, c := range cases {
		if got := IsUUIDFormat(c.in); got != c.want {
			t.Errorf("IsUUIDFormat(%q): want %v, got %v", c.in, c.want, got)
		}
	}
}

// TestCalculateStringEntropy pins the RUNE-level normalized Shannon entropy
// [ref: statistics.rs edgeCases]. Empty/single -> 0.0; all-identical -> 0.0
// (maxEntropy log2(1)=0, guarded); "ab" (50/50) -> 1.0.
func TestCalculateStringEntropy(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0.0},
		{"a", 0.0},
		{"aaaa", 0.0},
		{"ab", 1.0},
		{"abcd", 1.0}, // 4 distinct over 4 chars -> entropy 2 / maxEntropy log2(4)=2 -> 1.0
	}
	for _, c := range cases {
		if got := CalculateStringEntropy(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("CalculateStringEntropy(%q): want %v, got %v", c.in, c.want, got)
		}
	}
	// RUNE-level: a 2-distinct multibyte string behaves like "ab".
	if got := CalculateStringEntropy("αβ"); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("CalculateStringEntropy(αβ): want 1.0 (rune-level), got %v", got)
	}
}

// num builds a json.Number the way decodeJSON would, so DetectSequentialPattern
// tests exercise the real value types (Number/string/bool/nil).
func num(s string) json.Number { return json.Number(s) }

// TestDetectSequentialPattern pins the BUG#2-fixed sequential detector
// [ref: statistics.rs edgeCases, BUG#2]. Bools are never numeric; all-string
// numerics are categorical (false); one real Number flips the flag; avg-diff must
// sit in [0.5,2.0]; descending fails only when checkOrder is set.
func TestDetectSequentialPattern(t *testing.T) {
	// <5 values -> false.
	if DetectSequentialPattern([]any{num("1"), num("2"), num("3"), num("4")}, false) {
		t.Errorf("fewer than 5 values: want false")
	}

	// BUG#2: all-string numerics are categorical -> false even though 001..010 look sequential.
	allStr := []any{"001", "002", "003", "004", "005", "006", "007", "008", "009", "010"}
	if DetectSequentialPattern(allStr, false) {
		t.Errorf("all-string numerics (BUG#2): want false")
	}
	// One real Number flips hadNonStringNumeric -> eligible; unit-step -> true.
	oneReal := []any{num("1"), "002", "003", "004", "005"}
	if !DetectSequentialPattern(oneReal, false) {
		t.Errorf("one real Number among strings: want true")
	}

	// Bools are never numeric -> the numeric slice is empty -> false.
	bools := []any{true, false, true, false, true}
	if DetectSequentialPattern(bools, false) {
		t.Errorf("bools never numeric: want false")
	}

	// Plain ascending unit-step ints -> true.
	asc := []any{num("1"), num("2"), num("3"), num("4"), num("5"), num("6")}
	if !DetectSequentialPattern(asc, false) {
		t.Errorf("ascending unit-step ints: want true")
	}

	// avg-diff outside [0.5,2.0] (step of 5) -> false.
	bigStep := []any{num("0"), num("5"), num("10"), num("15"), num("20")}
	if DetectSequentialPattern(bigStep, false) {
		t.Errorf("avg-diff 5 outside [0.5,2.0]: want false")
	}

	// Descending: checkOrder=true rejects, checkOrder=false accepts.
	desc := []any{num("6"), num("5"), num("4"), num("3"), num("2"), num("1")}
	if DetectSequentialPattern(desc, true) {
		t.Errorf("descending with checkOrder: want false")
	}
	if !DetectSequentialPattern(desc, false) {
		t.Errorf("descending without checkOrder: want true")
	}

	// Float unit-step (1.5,2.5,...) -> true.
	floats := []any{num("1.5"), num("2.5"), num("3.5"), num("4.5"), num("5.5")}
	if !DetectSequentialPattern(floats, false) {
		t.Errorf("float unit-step: want true")
	}

	// Whitespace-padded numeric strings are still detected (with a real Number to pass BUG#2).
	padded := []any{num("1"), " 2 ", " 3 ", " 4 ", " 5 "}
	if !DetectSequentialPattern(padded, false) {
		t.Errorf("whitespace-padded numeric strings: want true")
	}
}

// TestPythonIntParse pins the Python int()-style parser [ref: statistics.rs
// edgeCases]. Accepts surrounding whitespace, +/- sign, and single underscores
// between digits; rejects leading/trailing/double underscores, floats, hex, sci.
func TestPythonIntParse(t *testing.T) {
	okCases := []struct {
		in   string
		want int64
	}{
		{"  5  ", 5},
		{"+5", 5},
		{"-5", -5},
		{"3_000", 3000},
		{"0", 0},
	}
	for _, c := range okCases {
		if got, ok := pythonIntParse(c.in); !ok || got != c.want {
			t.Errorf("pythonIntParse(%q): want (%d,true), got (%d,%v)", c.in, c.want, got, ok)
		}
	}
	failCases := []string{"_5", "5_", "3__000", "3.14", "0x1f", "1e3", "", "abc"}
	for _, s := range failCases {
		if _, ok := pythonIntParse(s); ok {
			t.Errorf("pythonIntParse(%q): want ok=false", s)
		}
	}
}
