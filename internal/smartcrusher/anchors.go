package smartcrusher

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// Query-anchor extraction and matching [ref: anchors.rs]. "Anchors" are salient
// query tokens (UUIDs, 4+ digit IDs, hostnames, quoted strings, emails); an item
// is preserved if its Python-repr string contains any anchor as a substring.
// Upstream marks this "deprecated" in favor of the RelevanceScorer, but it still
// runs on every live SmartCrusher invocation, so it is MVP. No CCR, no byte-
// saving math, no float formatting here.

// Five anchor patterns, compiled once at init. All are RE2-safe (ASCII \b).
var (
	// UUID_PATTERN — 8-4-4-4-12 hex, case-insensitive; result lowercased.
	uuidPattern = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	// NUMERIC_ID_PATTERN — 4+ digits (3-digit NOT matched); kept as-is.
	numericIDPattern = regexp.MustCompile(`\b\d{4,}\b`)
	// HOSTNAME_PATTERN — label.label(.tld)?; lowercased, blocklist-filtered.
	hostnamePattern = regexp.MustCompile(`\b[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9]*(?:\.[a-zA-Z]{2,})?\b`)
	// QUOTED_STRING_PATTERN — inner group 1, 1..50 non-quote bytes; kept if the
	// TRIMMED inner is >=2 bytes, but the STORED anchor is the un-trimmed inner.
	quotedStringPattern = regexp.MustCompile(`['"]([^'"]{1,50})['"]`)
	// EMAIL_PATTERN — reproduces the upstream literal '|' in the TLD class; it is
	// a harmless typo (RE2 treats '|' as an alternation that also matches the
	// single-char '|'), behaviorally identical to [A-Za-z]{2,} for real emails.
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`)
)

// hostnameFalsePositives are dropped when a lowercased hostname match equals one
// of them EXACTLY [ref: anchors.rs HOSTNAME_FALSE_POSITIVES]. "e.g" is dropped;
// "e.g.com" is not (it is not an exact entry).
var hostnameFalsePositives = map[string]struct{}{
	"e.g":  {},
	"i.e":  {},
	"etc.": {},
}

// quotedTrimMinLen is the minimum BYTE length (Go len, not rune count) of the
// TRIMMED inner of a quoted string for it to be kept as an anchor.
const quotedTrimMinLen = 2

// ExtractQueryAnchors returns the lowercased anchor set for text. The set is a
// map[string]struct{} because order is irrelevant on the query side (this is not
// the compression path). Empty text yields an empty set. Each regex runs over the
// FULL original text independently, so a substring may contribute to multiple
// categories [ref: anchors.rs extract_query_anchors].
func ExtractQueryAnchors(text string) map[string]struct{} {
	anchors := make(map[string]struct{})
	if text == "" {
		return anchors
	}

	// UUIDs: insert lowercased.
	for _, m := range uuidPattern.FindAllString(text, -1) {
		anchors[strings.ToLower(m)] = struct{}{}
	}
	// Numeric IDs: insert as-is (the only category not lowercased).
	for _, m := range numericIDPattern.FindAllString(text, -1) {
		anchors[m] = struct{}{}
	}
	// Hostnames: lowercase, then drop exact blocklist matches.
	for _, m := range hostnamePattern.FindAllString(text, -1) {
		lc := strings.ToLower(m)
		if _, blocked := hostnameFalsePositives[lc]; blocked {
			continue
		}
		anchors[lc] = struct{}{}
	}
	// Quoted strings: length gate on the TRIMMED inner, but store the UN-trimmed
	// inner lowercased.
	for _, m := range quotedStringPattern.FindAllStringSubmatch(text, -1) {
		inner := m[1]
		if len(strings.TrimSpace(inner)) >= quotedTrimMinLen {
			anchors[strings.ToLower(inner)] = struct{}{}
		}
	}
	// Emails: insert lowercased.
	for _, m := range emailPattern.FindAllString(text, -1) {
		anchors[strings.ToLower(m)] = struct{}{}
	}
	return anchors
}

// ItemMatchesAnchors reports whether the Python-repr of item (lowercased)
// contains any anchor as a substring [ref: anchors.rs item_matches_anchors]. An
// empty anchor set never matches. pythonRepr includes single-quoted object keys,
// so matching tests BOTH keys and values.
func ItemMatchesAnchors(item *orderedmap.OrderedMap, anchors map[string]struct{}) bool {
	if len(anchors) == 0 {
		return false
	}
	itemStr := strings.ToLower(pythonRepr(item))
	for anchor := range anchors {
		if strings.Contains(itemStr, anchor) {
			return true
		}
	}
	return false
}

// pythonRepr renders value in Python-str style for anchor SUBSTRING matching
// [ref: anchors.rs python_repr]: null->None, true->True, false->False, numbers
// natural (json.Number literal), strings ALWAYS single-quoted (the parity gap —
// Python would switch to double quotes when the string contains a single quote,
// but upstream does not, so neither do we), arrays "[a, b, c]", objects
// "{'k': v, 'k2': v2}" with single-quoted keys in INSERTION order.
//
// This is NOT analyzer.go's cardinalityRepr: that one renders objects/arrays as
// JSON and strings raw. They are intentionally distinct unexported helpers in the
// same package (see Shared Contract "Two value-stringifiers").
func pythonRepr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case json.Number:
		return v.String()
	case string:
		// Always single-quoted; no escaping (reproduces the always-single drift).
		return "'" + v + "'"
	case []any:
		parts := make([]string, len(v))
		for i, elem := range v {
			parts[i] = pythonRepr(elem)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *orderedmap.OrderedMap:
		keys := v.Keys()
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			val, _ := v.Get(k)
			parts = append(parts, "'"+k+"': "+pythonRepr(val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		// decodeJSON only produces the cases above; anything else is a programmer
		// error, so fall back to a stable rendering rather than panic.
		return "None"
	}
}
