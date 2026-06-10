package compress

import "testing"

func eqStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSplitLinesRust(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\nb", []string{"a", "b"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\n", []string{"a"}},           // \r\n terminator strips \r
		{"a\r\nb\r\n", []string{"a", "b"}}, // both terminated
		{"a\r\nb\r", []string{"a", "b\r"}}, // bare trailing \r on unterminated last line is preserved
		{"a\rb", []string{"a\rb"}},         // lone interior \r not before \n is preserved
	}
	for _, c := range cases {
		if got := splitLinesRust(c.in); !eqStrSlice(got, c.want) {
			t.Errorf("splitLinesRust(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestASCIILower(t *testing.T) {
	if got := asciiLower("ABCxyz123"); got != "abcxyz123" {
		t.Errorf("asciiLower = %q", got)
	}
	// Non-ASCII bytes must be left untouched (unlike Unicode strings.ToLower):
	// İ (U+0130, 2 bytes) would change under ToLower but must not here.
	for _, in := range []string{"İ", "Ω", "ÉÀ"} {
		if got := asciiLower(in); got != in {
			t.Errorf("asciiLower(%q) = %q, want non-ASCII untouched", in, got)
		}
	}
}
