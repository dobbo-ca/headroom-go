package reformats

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestJsonMinifierName(t *testing.T) {
	if got := (JsonMinifier{}).Name(); got != "json_minifier" {
		t.Errorf("Name = %q, want json_minifier", got)
	}
}

func TestJsonMinifierAppliesToJsonArray(t *testing.T) {
	types := (JsonMinifier{}).AppliesTo()
	if len(types) != 1 || types[0] != transform.JsonArray {
		t.Errorf("AppliesTo = %v, want [JsonArray]", types)
	}
}

func TestJsonMinifierStripsWhitespace(t *testing.T) {
	in := "[\n  1,\n  2,\n  3\n]"
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != "[1,2,3]" {
		t.Errorf("Output = %q, want [1,2,3]", out.Output)
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Errorf("BytesSaved = %d, want %d", out.BytesSaved, len(in)-len(out.Output))
	}
}

func TestJsonMinifierPreservesNumericLiterals(t *testing.T) {
	// UseNumber keeps the exact text. Without it, 1.0 becomes 1 and a large
	// integer loses precision through float64.
	in := `[1.0, 1e3, 12345678901234567890]`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for _, want := range []string{"1.0", "1e3", "12345678901234567890"} {
		if !strings.Contains(out.Output, want) {
			t.Errorf("Output %q lost literal %q", out.Output, want)
		}
	}
}

func TestJsonMinifierDoesNotEscapeHTML(t *testing.T) {
	in := `{"k": "a<b>c&d"}`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, `<`) {
		t.Errorf("Output %q escaped HTML; SetEscapeHTML(false) not applied", out.Output)
	}
}

func TestJsonMinifierNeverInflates(t *testing.T) {
	// Already minimal: re-encoding cannot be shorter, so the input comes back
	// with BytesSaved 0.
	in := `[1,2,3]`
	out, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if out.Output != in {
		t.Errorf("Output = %q, want the input %q back", out.Output, in)
	}
	if out.BytesSaved != 0 {
		t.Errorf("BytesSaved = %d, want 0", out.BytesSaved)
	}
}

func TestJsonMinifierRejectsInvalidJSON(t *testing.T) {
	_, err := (JsonMinifier{}).Apply("not json at all")
	if !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestJsonMinifierIsDeterministic(t *testing.T) {
	// Map key order must not vary between runs. encoding/json sorts map keys,
	// which is what makes this hold.
	in := `{"z":1,"a":2,"m":3,"b":4,"y":5}`
	first, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	for i := 0; i < 20; i++ {
		next, err := (JsonMinifier{}).Apply(in)
		if err != nil {
			t.Fatalf("Apply returned error: %v", err)
		}
		if next.Output != first.Output {
			t.Fatalf("run %d gave %q, first gave %q", i, next.Output, first.Output)
		}
	}
}

func TestJsonMinifierEmptyInputIsInvalid(t *testing.T) {
	if _, err := (JsonMinifier{}).Apply(""); !errors.Is(err, transform.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}
