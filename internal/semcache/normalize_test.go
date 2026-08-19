package semcache

import "testing"

func TestNormalizeCollapsesWhitespace(t *testing.T) {
	if got, want := Normalize("read   the\t\tfile"), "read the file"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeTrimsEnds(t *testing.T) {
	if got, want := Normalize("  hello  "), "hello"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeCollapsesNewlines(t *testing.T) {
	if got, want := Normalize("a\n\n\nb"), "a b"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeLowercases(t *testing.T) {
	if got, want := Normalize("Read The File"), "read the file"; got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	once := Normalize("  Mixed   Case\n\ttext ")
	if twice := Normalize(once); once != twice {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}

func TestNormalizeEmptyStaysEmpty(t *testing.T) {
	if got := Normalize("   \n\t "); got != "" {
		t.Errorf("Normalize = %q, want empty", got)
	}
}
