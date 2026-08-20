// Package reformats holds lossless transforms: the output is semantically
// equivalent to the input and there is no CCR backing.
package reformats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// JsonMinifier removes insignificant whitespace from JSON.
//
// Go's encoding/json reorders object keys, a divergence the core design
// accepts. The never-inflate guard is what makes it safe: a reordering that
// grows the payload is discarded and the input is returned unchanged.
type JsonMinifier struct{}

func (JsonMinifier) Name() string { return "json_minifier" }

func (JsonMinifier) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.JsonArray}
}

// Apply minifies content. UseNumber keeps numeric literals as their exact
// source text; without it 1.0 becomes 1 and large integers lose precision
// through float64. SetEscapeHTML(false) stops <, > and & expanding to \u00xx,
// which would inflate the output.
func (JsonMinifier) Apply(content string) (transform.ReformatOutput, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("json_minifier: decode: %w", transform.ErrInvalidInput)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return transform.ReformatOutput{}, fmt.Errorf("json_minifier: encode: %w", transform.ErrInternal)
	}

	// Encoder.Encode appends a newline that the input did not have.
	out := strings.TrimSuffix(buf.String(), "\n")

	if len(out) >= len(content) {
		return transform.ReformatOutput{Output: content, BytesSaved: 0}, nil
	}
	return transform.ReformatOutput{Output: out, BytesSaved: len(content) - len(out)}, nil
}
