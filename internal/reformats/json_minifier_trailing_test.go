package reformats

import (
	"errors"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Upstream serde_json::from_str rejects any trailing characters after the JSON
// value. Go's json.Decoder consumes only the first value, so without an explicit
// EOF check it would silently drop trailing bytes (e.g. minify `{}garbage` to
// `{}`). These must be rejected as invalid input.
func TestJSONMinifierRejectsTrailingData(t *testing.T) {
	var m JsonMinifier
	for _, in := range []string{`{}garbage`, `[1,2]extra`, `{"a":1} {"b":2}`, `1 2`} {
		if _, err := m.Apply(in); !errors.Is(err, transform.ErrInvalidInput) {
			t.Errorf("Apply(%q): want ErrInvalidInput, got %v", in, err)
		}
	}
	// A clean single value (already compact) still round-trips without error.
	out, err := m.Apply(`{"a":1}`)
	if err != nil || out.Output != `{"a":1}` {
		t.Fatalf("clean value: out=%q err=%v", out.Output, err)
	}
}
