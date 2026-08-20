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
	// Assert the exact minified output, not just the presence of "<": if
	// SetEscapeHTML(false) is deleted the escaped encoding is longer than
	// the input, so the never-inflate guard would return the raw
	// (unminified but unescaped) input and a substring check would miss it.
	if want := `{"k":"a<b>c&d"}`; out.Output != want {
		t.Errorf("Output = %q, want %q", out.Output, want)
	}
}

func TestJsonMinifierNeverInflates(t *testing.T) {
	// A literal U+2028 inside a string re-encodes as the 6-byte
	// escape, so the encoded form is strictly longer than the input. This
	// is what actually exercises the len(out) >= len(content) guard; an
	// already-minimal input like [1,2,3] re-encodes byte-identically and
	// would pass even if the guard were deleted.
	in := "[\" \"]"
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

func TestJsonMinifierRejectsTrailingContent(t *testing.T) {
	// json.Decoder.Decode only consumes the first JSON value; without an
	// explicit dec.More() check, trailing bytes after that value are
	// silently discarded, which would be data loss for a lossless
	// transform with no CCR backing.
	for _, in := range []string{
		`{"a":1}   and then some prose the model wrote`,
		`[1,2,3]xyz`,
	} {
		_, err := (JsonMinifier{}).Apply(in)
		if !errors.Is(err, transform.ErrInvalidInput) {
			t.Errorf("Apply(%q) err = %v, want ErrInvalidInput", in, err)
		}
	}
}

func TestJsonMinifierIsDeterministic(t *testing.T) {
	// Map key order must not vary between runs. encoding/json sorts map keys,
	// which is what makes this hold. The input has whitespace so the
	// re-encoded form is what gets compared, not the never-inflate guard's
	// verbatim passthrough.
	in := `{"z": 1, "a": 2, "m": 3, "b": 4, "y": 5}`
	first, err := (JsonMinifier{}).Apply(in)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if first.BytesSaved <= 0 {
		t.Fatalf("BytesSaved = %d, want > 0 so the encoded path (not the never-inflate guard) is what's compared", first.BytesSaved)
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

func TestJsonMinifierNeverPanics(t *testing.T) {
	malformed := []string{
		"",
		"{",
		"\x00\xff\xfe",
		`"\ud800"`,
		"1e99999999",
		"\xc3\x28", // invalid UTF-8
	}
	for _, in := range malformed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Apply(%q) panicked: %v", in, r)
				}
			}()
			_, err := (JsonMinifier{}).Apply(in)
			if err == nil {
				return
			}
			if !errors.Is(err, transform.ErrInvalidInput) && !errors.Is(err, transform.ErrInternal) {
				t.Errorf("Apply(%q) err = %v, want a transform sentinel error", in, err)
			}
		}()
	}
}
