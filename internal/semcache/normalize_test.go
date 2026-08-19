package semcache

import "testing"

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"read   the\t\tfile", "read the file"},
		{"  hello  ", "hello"},
		{"a\n\n\nb", "a b"},
		{"Read The File", "read the file"},
		{"   \n\t ", ""},
	} {
		got := Normalize(tc.in)
		if got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if again := Normalize(got); again != tc.want {
			t.Errorf("Normalize(%q) not idempotent: %q", tc.in, again)
		}
	}
}
