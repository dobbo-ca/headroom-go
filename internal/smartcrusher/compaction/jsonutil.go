package compaction

import (
	"encoding/json"

	"github.com/iancoleman/orderedmap"
)

// This file holds the ordered JSON decoder local to the compaction package. It is
// intentionally NOT the parent smartcrusher.decodeJSON: importing the parent would
// create an import cycle (the parent depends on this sub-package). It mirrors the
// same contract — objects become *orderedmap.OrderedMap in insertion order, arrays
// become []any, numbers become json.Number (UseNumber) — so key order and numeric
// literals are preserved on the compression path (map[string]any is BANNED).

// decodeValue decodes the next JSON value from the token stream.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeFromToken(dec, tok)
}

// decodeFromToken continues decoding given an already-read leading token.
func decodeFromToken(dec *json.Decoder, tok json.Token) (any, error) {
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		}
		return nil, errUnexpectedDelim
	}
	// json.Number, string, bool, or nil — already the shapes we want.
	return tok, nil
}

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
			return nil, errNonStringKey
		}
		val, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		om.Set(key, val)
	}
}

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

// sentinel decode errors (kept as vars so the decoder returns without fmt).
var (
	errUnexpectedDelim = jsonDecodeError("compaction: unexpected delimiter")
	errNonStringKey    = jsonDecodeError("compaction: object key is not a string")
)

type jsonDecodeError string

func (e jsonDecodeError) Error() string { return string(e) }
