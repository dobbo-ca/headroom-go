package smartcrusher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// decodeJSON is the shared ordered-decode helper (porting rule 1). It decodes s
// into a Go value where every JSON object becomes a *orderedmap.OrderedMap with
// keys in parse/insertion order, every JSON array becomes a []any, and every
// JSON number becomes a json.Number (UseNumber) so integer literals stay integer-
// typed and nothing is reformatted. Downstream tasks reuse this instead of hand-
// rolling a decoder.
//
// The orderedmap library's own UnmarshalJSON is deliberately NOT used: it decodes
// numbers as float64 and nests objects as value-typed OrderedMap, neither of
// which matches the contract above. We drive json.Decoder token stream directly.
func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()

	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	// Reject trailing garbage after a single top-level value.
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("smartcrusher: unexpected trailing data after JSON value")
		}
		return nil, err
	}
	return v, nil
}

// decodeValue decodes the next JSON value from the token stream. It returns
// *orderedmap.OrderedMap for objects, []any for arrays, json.Number for numbers,
// and string/bool/nil for the remaining scalars.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeFromToken(dec, tok)
}

// decodeFromToken continues decoding given an already-read leading token.
func decodeFromToken(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		default:
			return nil, fmt.Errorf("smartcrusher: unexpected delimiter %q", t)
		}
	default:
		// json.Number, string, bool, or nil — already the shapes we want.
		return tok, nil
	}
}

// decodeObject decodes an object whose leading '{' has been consumed, preserving
// key insertion order and recursing with decodeValue for each value.
func decodeObject(dec *json.Decoder) (*orderedmap.OrderedMap, error) {
	om := orderedmap.New()
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			return om, nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("smartcrusher: object key is not a string: %v", tok)
		}
		val, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		om.Set(key, val)
	}
}

// decodeArray decodes an array whose leading '[' has been consumed.
func decodeArray(dec *json.Decoder) ([]any, error) {
	arr := []any{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if d, ok := tok.(json.Delim); ok && d == ']' {
			return arr, nil
		}
		val, err := decodeFromToken(dec, tok)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
}

// pythonSafeJSONDumps renders v as compact JSON: `,`/`:` separators with NO
// surrounding spaces, non-ASCII left un-escaped, and object key insertion order
// preserved [ref: crusher.rs python_safe_json_dumps]. It mirrors Python's
// json.dumps(separators=(",",":"), ensure_ascii=False). The value must already
// be in the decodeJSON shape (*orderedmap.OrderedMap objects, []any arrays,
// json.Number numbers) so key order and numeric literals are preserved.
//
// encoding/json already emits compact output with no inter-element spaces and
// orderedmap.OrderedMap marshals in insertion order, so a single Marshal with
// HTML escaping disabled satisfies the contract.
func pythonSafeJSONDumps(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// The compression path only feeds decodeJSON-shaped values here, which
		// always marshal; a failure is a programmer error, not runtime input.
		return ""
	}
	// json.Encoder.Encode appends a trailing newline; strip it.
	return strings.TrimRight(buf.String(), "\n")
}

// compactSerialize renders a non-string crusher result via the same compact
// serializer as pythonSafeJSONDumps [ref: crusher.rs]. It is a named alias kept
// distinct so callers reading the crusher read as "serialize the crushed value".
func compactSerialize(v any) string {
	return pythonSafeJSONDumps(v)
}
