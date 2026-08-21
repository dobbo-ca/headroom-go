package compaction

import (
	"encoding/json"
	"io"
	"strings"
	"unicode"

	"github.com/iancoleman/orderedmap"
)

// This file implements the per-cell classifier that decides which cells become
// opaque CCR cells [ref: compaction/classifier.rs]. It is conservative: when in
// doubt it returns Scalar. It is NOT homogeneity detection (that is the caller's
// job). Light and pure — no float formatting, no markers, no recursion beyond the
// single stringified-JSON parse.

// CellClassKind is the discriminant of CellClass.
type CellClassKind int

const (
	// ClassScalar is a plain scalar cell.
	ClassScalar CellClassKind = iota
	// ClassJsonObject is a JSON object value.
	ClassJsonObject
	// ClassJsonArray is a JSON array value.
	ClassJsonArray
	// ClassStringifiedJson is a string whose content parsed to an object/array.
	ClassStringifiedJson
	// ClassOpaque is a bulky string routed to a CCR cell (see OpaqueKind).
	ClassOpaque
)

// CellClass is the classification result. Parsed is set only for
// ClassStringifiedJson (the decoded object/array, avoiding a downstream
// re-parse); OpaqueKind is set only for ClassOpaque.
type CellClass struct {
	Kind       CellClassKind
	Parsed     any
	OpaqueKind OpaqueKind
}

// ClassifyConfig tunes the opaque-cell heuristics.
type ClassifyConfig struct {
	// OpaqueMinBytes is the strict lower bound (BYTE length) a string must exceed
	// to be an opaque candidate.
	OpaqueMinBytes int
	// Base64AlphabetRatio is the minimum fraction of base64-alphabet chars.
	Base64AlphabetRatio float64
	// HtmlMinOpenBrackets is the minimum '<' count AND tag-start count for HTML.
	HtmlMinOpenBrackets int
}

// DefaultClassifyConfig returns the upstream defaults (256, 0.95, 3).
func DefaultClassifyConfig() ClassifyConfig {
	return ClassifyConfig{
		OpaqueMinBytes:      256,
		Base64AlphabetRatio: 0.95,
		HtmlMinOpenBrackets: 3,
	}
}

// hardcoded classifier constants (not configurable) [ref: classifier.rs constants].
const (
	base64MinLen        = 64 // BYTE length floor for base64 detection.
	base64UniqueCharMin = 16 // unique-char floor; reaching it early-exits true.
)

// classifyCell classifies a single decodeJSON-shaped value. Objects and arrays
// classify by structure; strings run classifyString; everything else is Scalar.
func classifyCell(value any, cfg ClassifyConfig) CellClass {
	switch v := value.(type) {
	case *orderedmap.OrderedMap:
		return CellClass{Kind: ClassJsonObject}
	case []any:
		return CellClass{Kind: ClassJsonArray}
	case string:
		return classifyString(v, cfg)
	default:
		return CellClass{Kind: ClassScalar}
	}
}

// classifyString runs the ordered, first-match string classification.
func classifyString(s string, cfg ClassifyConfig) CellClass {
	// STEP1 stringified-JSON: gate on the trimmed leading char, but parse the
	// ORIGINAL untrimmed string. Only object/array results qualify.
	trimmed := strings.TrimLeftFunc(s, unicode.IsSpace)
	if r := firstRune(trimmed); r == '{' || r == '[' {
		if parsed, ok := parseContainer(s); ok {
			return CellClass{Kind: ClassStringifiedJson, Parsed: parsed}
		}
	}

	// STEP2 length gate: strict '>' to proceed (BYTE length).
	if len(s) <= cfg.OpaqueMinBytes {
		return CellClass{Kind: ClassScalar}
	}

	// STEP3 base64.
	if looksLikeBase64(s, cfg.Base64AlphabetRatio) {
		return CellClass{Kind: ClassOpaque, OpaqueKind: Base64Blob}
	}

	// STEP4 html (checked after base64 so real base64 with no '<' wins).
	if looksLikeHtml(s, cfg.HtmlMinOpenBrackets) {
		return CellClass{Kind: ClassOpaque, OpaqueKind: HtmlChunk}
	}

	// STEP5 fallback.
	return CellClass{Kind: ClassOpaque, OpaqueKind: LongString}
}

// firstRune returns the first rune of s, or utf8.RuneError-equivalent 0 if empty.
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// parseContainer decodes the ORIGINAL string and reports whether it is a JSON
// object or array (bare scalars fail). It uses a decoder local to this package so
// compaction never imports its parent smartcrusher package.
func parseContainer(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	switch v.(type) {
	case *orderedmap.OrderedMap, []any:
		return v, true
	default:
		return nil, false
	}
}

// looksLikeBase64 reports whether s looks like a base64 blob: at least 64 bytes,
// no '<'/'>', no Unicode whitespace, at least ratio of chars in the base64
// alphabet, and at least 16 distinct chars. Disqualifiers are checked before the
// ratio/diversity scan so they short-circuit false.
func looksLikeBase64(s string, ratio float64) bool {
	if len(s) < base64MinLen {
		return false
	}
	if strings.ContainsRune(s, '<') || strings.ContainsRune(s, '>') {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			return false
		}
	}

	total := len(s) // BYTE count.
	alphabet := 0   // CHAR count in the base64 alphabet (identical for ASCII).
	for _, r := range s {
		if isBase64AlphabetChar(r) {
			alphabet++
		}
	}
	if float64(alphabet)/float64(total) < ratio {
		return false
	}

	// Diversity: accumulate unique chars; reaching >=16 unique returns true
	// immediately. Consuming the whole string without 16 unique -> false.
	seen := make(map[rune]struct{}, base64UniqueCharMin)
	for _, r := range s {
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			if len(seen) >= base64UniqueCharMin {
				return true
			}
		}
	}
	return false
}

// isBase64AlphabetChar reports whether r is ASCII-alphanumeric or one of the
// base64 extras (+ / = _ -).
func isBase64AlphabetChar(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= 'a' && r <= 'z':
		return true
	case r == '+' || r == '/' || r == '=' || r == '_' || r == '-':
		return true
	default:
		return false
	}
}

// looksLikeHtml reports whether s has at least minOpen '<' characters AND at least
// minOpen of them are tag-starts (next byte is ASCII-alpha, '/', or '!'). It walks
// bytes with an i+1 lookahead.
func looksLikeHtml(s string, minOpen int) bool {
	opens := strings.Count(s, "<")
	if opens < minOpen {
		return false
	}
	b := []byte(s)
	tagStarts := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '<' {
			continue
		}
		if i+1 >= len(b) {
			continue
		}
		next := b[i+1]
		if isASCIIAlpha(next) || next == '/' || next == '!' {
			tagStarts++
		}
	}
	return tagStarts >= minOpen
}

// isASCIIAlpha reports whether b is an ASCII letter (NOT unicode.IsLetter).
func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
