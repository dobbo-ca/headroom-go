package reformats

import (
	"errors"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestJSONMinifierStripsWhitespace(t *testing.T) {
	var m JsonMinifier
	in := "[ {\n  \"a\": 1,\n  \"b\": 2\n} ]"
	out, err := m.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != `[{"a":1,"b":2}]` {
		t.Fatalf("got %q", out.Output)
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Fatalf("bytes_saved = %d", out.BytesSaved)
	}
}

func TestJSONMinifierNeverInflates(t *testing.T) {
	var m JsonMinifier
	for _, in := range []string{`{}`, `[]`, `null`, `42`, `{"a":1,"b":2}`} {
		out, err := m.Apply(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Output) > len(in) {
			t.Fatalf("inflated %q -> %q", in, out.Output)
		}
	}
}

func TestJSONMinifierEmptySkipped(t *testing.T) {
	var m JsonMinifier
	if _, err := m.Apply("   \n\t "); !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("want ErrSkipped, got %v", err)
	}
}

func TestJSONMinifierInvalid(t *testing.T) {
	var m JsonMinifier
	if _, err := m.Apply(`{not valid`); !errors.Is(err, transform.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestJSONMinifierHTMLNotEscaped(t *testing.T) {
	var m JsonMinifier
	out, err := m.Apply(`["<a> & <b>"]`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != `["<a> & <b>"]` {
		t.Fatalf("HTML must not be escaped: %q", out.Output)
	}
}

var _ transform.ReformatTransform = JsonMinifier{}
