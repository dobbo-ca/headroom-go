// Package reformats holds lossless ReformatTransform implementations that pack
// content denser without dropping information.
package reformats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// jsonMinifierName is the transform name, stamped into every wrapped error.
const jsonMinifierName = "json_minifier"

// JsonMinifier is a whitespace-stripping JSON minifier implementing
// transform.ReformatTransform. It handles both JSON arrays and objects (the
// detector folds both into the JsonArray content type), parsing the input to a
// generic value and re-emitting it in compact form.
type JsonMinifier struct{}

// Name returns the transform name "json_minifier".
func (JsonMinifier) Name() string { return jsonMinifierName }

// AppliesTo returns the single content type this reformat applies to. JsonArray
// is the umbrella tag for all structurally-recognized JSON (arrays AND objects).
func (JsonMinifier) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.JsonArray}
}

// Apply trims, parses, re-emits compact, and applies a never-inflate guard. The
// guard and the fallback compare against the RAW (untrimmed) content; bytes_saved
// is a byte delta.
func (JsonMinifier) Apply(content string) (transform.ReformatOutput, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return transform.ReformatOutput{}, fmt.Errorf("%s skipped: empty input: %w", jsonMinifierName, transform.ErrSkipped)
	}

	// Parse the TRIMMED string. UseNumber preserves the numeric literal text
	// exactly, avoiding precision/format drift.
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("invalid input for %s: %s: %w", jsonMinifierName, err, transform.ErrInvalidInput)
	}
	// Require the whole input to be a single JSON value: serde_json::from_str
	// rejects trailing characters, but json.Decoder consumes only the first
	// value and would silently drop the rest. Reject anything after EOF.
	if _, err := dec.Token(); err != io.EOF {
		return transform.ReformatOutput{}, fmt.Errorf("invalid input for %s: trailing characters: %w", jsonMinifierName, transform.ErrInvalidInput)
	}

	// Re-emit compact with HTML escaping OFF (matches serde: no \u escaping of
	// <, >, &), so HTML-ish strings don't spuriously inflate.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("%s internal error: %s: %w", jsonMinifierName, err, transform.ErrInternal)
	}
	// Encoder appends a trailing newline.
	min := strings.TrimRight(buf.String(), "\n")

	// Never-inflate guard vs RAW content length. On inflate, return the RAW
	// original (with any surrounding whitespace) and bytes_saved == 0.
	if len(min) >= len(content) {
		return transform.ReformatOutput{Output: content, BytesSaved: 0}, nil
	}
	return transform.ReformatOutput{Output: min, BytesSaved: len(content) - len(min)}, nil
}
