// Package cachestab observes prompt-cache stability. Everything here is
// READ-ONLY: no function in this package returns a request body, and none is
// wired anywhere that could mutate one. The proxy's byte-faithfulness
// invariants (I1/I2/I3) are preserved by construction, not by care.
//
// Two detectors, ported from upstream headroom's Phase E:
//
//   - Volatile content (upstream PR-E5): content in the cached prefix that
//     changes every request, so any cache hit on it is accidental.
//   - Cache-bust drift (upstream PR-E6): a prefix that WAS stable and then
//     changed, which silently re-writes the whole cache at the customer's
//     expense. See drift.go.
package cachestab

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// MaxFindings caps findings per request. A CSV pasted into a system
	// prompt would otherwise produce hundreds of warnings and drown the log.
	// The first few are the ones anyone acts on.
	MaxFindings = 10
	// SampleBytes caps the excerpt logged per finding: enough to locate the
	// content, never enough to be bulk customer data.
	SampleBytes = 80
)

// idFieldNeedles are key-name substrings that conventionally hold a
// per-request unique ID. Matched case-insensitively. These catch values the
// timestamp and UUID scanners miss, such as integer trace IDs and custom slug
// formats.
var idFieldNeedles = []string{"request_id", "trace_id", "session_id", "correlation_id"}

// VolatileKind names what was found. These strings appear in logs and
// dashboards filter on them; do not change one without a deprecation note.
type VolatileKind string

const (
	// KindTimestamp is an ISO-8601 timestamp.
	KindTimestamp VolatileKind = "iso8601_timestamp"
	// KindUUID is a version-4 UUID.
	KindUUID VolatileKind = "uuid_v4"
	// KindIDField is a JSON key named like a per-request identifier.
	KindIDField VolatileKind = "id_field"
)

// Finding is one piece of volatile content. Location is a path such as
// "messages[0].content[1].text" so the customer can map it back to their
// request shape.
type Finding struct {
	Kind     VolatileKind
	Location string
	Sample   string
}

// DetectVolatile walks an Anthropic /v1/messages body for content that busts
// prompt-cache hits, and returns at most MaxFindings findings.
//
// It takes the raw bytes and never returns them. Scanning order is fixed —
// system, messages, tools — so the findings a given body produces are
// deterministic, which is what lets a test assert on them.
func DetectVolatile(body []byte) []Finding {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil
	}
	var out []Finding

	scanValue(root.Get("system"), "system", &out)

	if msgs := root.Get("messages"); msgs.IsArray() {
		for i, msg := range msgs.Array() {
			if len(out) >= MaxFindings {
				return out
			}
			scanValue(msg.Get("content"), "messages["+strconv.Itoa(i)+"].content", &out)
		}
	}

	// tools are deliberately NOT scanned. Upstream scans them; measured
	// against real Claude Code traffic that is pure noise. A tool definition
	// is static by construction, so its ISO-8601 examples ("e.g.
	// 2026-03-09T09:00:00") and its id-named SCHEMA PROPERTIES
	// (properties.session_id declares a field, it does not carry a value)
	// are byte-identical every request and cannot bust a cache. On one real
	// request they consumed 6 of the 10 finding slots and crowded out 4
	// genuine per-request UUIDs.
	//
	// Nothing is lost: a tools array that really does change between turns
	// is exactly what the E6 drift detector's tools axis reports, and it
	// reports it from observed change rather than guessed shape.
	return out
}

// scanValue handles a field that may be a string, an array of content blocks,
// or an object.
func scanValue(v gjson.Result, location string, out *[]Finding) {
	if len(*out) >= MaxFindings || !v.Exists() {
		return
	}
	switch {
	case v.Type == gjson.String:
		scanString(v.String(), location, out)
	case v.IsArray():
		for i, item := range v.Array() {
			if len(*out) >= MaxFindings {
				return
			}
			scanRecursive(item, location+"["+strconv.Itoa(i)+"]", out)
		}
	case v.IsObject():
		scanRecursive(v, location, out)
	}
}

// scanRecursive walks a subtree for both volatile strings and ID-named keys.
// It is the only walker that inspects key names.
func scanRecursive(v gjson.Result, location string, out *[]Finding) {
	if len(*out) >= MaxFindings || !v.Exists() {
		return
	}
	switch {
	case v.Type == gjson.String:
		scanString(v.String(), location, out)
	case v.IsArray():
		for i, item := range v.Array() {
			if len(*out) >= MaxFindings {
				return
			}
			scanRecursive(item, location+"["+strconv.Itoa(i)+"]", out)
		}
	case v.IsObject():
		v.ForEach(func(key, sub gjson.Result) bool {
			if len(*out) >= MaxFindings {
				return false
			}
			k := key.String()
			if isIDNamedKey(k) && !isEmptyValue(sub) {
				*out = append(*out, Finding{
					Kind:     KindIDField,
					Location: location + "." + k,
					Sample:   truncate(sampleOf(sub)),
				})
				if len(*out) >= MaxFindings {
					return false
				}
			}
			scanRecursive(sub, location+"."+k, out)
			return true
		})
	}
}

// scanString finds ISO-8601 timestamps and v4 UUIDs by byte position. No
// regexp: the shapes are fixed-width, so explicit checks are both faster and
// clearer about what counts as a match.
func scanString(s, location string, out *[]Finding) {
	b := []byte(s)
	for i := 0; i < len(b); {
		if len(*out) >= MaxFindings {
			return
		}
		// ISO-8601 first: the shorter window means a string ending
		// mid-UUID still yields the timestamp it contains.
		if i+19 <= len(b) && looksLikeISO8601(b[i:i+19]) {
			*out = append(*out, Finding{
				Kind: KindTimestamp, Location: location, Sample: truncate(s[i : i+19])})
			i += 19
			continue
		}
		if i+36 <= len(b) && looksLikeUUIDv4(b[i:i+36]) {
			*out = append(*out, Finding{
				Kind: KindUUID, Location: location, Sample: truncate(s[i : i+36])})
			i += 36
			continue
		}
		i++
	}
}

func isDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// looksLikeISO8601 reports whether the 19-byte window is YYYY-MM-DDTHH:MM:SS.
// A space separator is accepted: RFC 3339 section 5.6 permits it, and log
// lines use it constantly.
func looksLikeISO8601(w []byte) bool {
	if len(w) < 19 {
		return false
	}
	return isDigits(w[0:4]) && w[4] == '-' &&
		isDigits(w[5:7]) && w[7] == '-' &&
		isDigits(w[8:10]) &&
		(w[10] == 'T' || w[10] == 't' || w[10] == ' ') &&
		isDigits(w[11:13]) && w[13] == ':' &&
		isDigits(w[14:16]) && w[16] == ':' &&
		isDigits(w[17:19])
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// looksLikeUUIDv4 reports whether the 36-byte window is a v4 UUID. The version
// nibble at 14 and the variant nibble at 19 are what separate a per-request
// UUID from a build hash or any other 32-hex-digit string, which would not
// change between requests and so is not volatile.
func looksLikeUUIDv4(w []byte) bool {
	if len(w) < 36 {
		return false
	}
	if w[8] != '-' || w[13] != '-' || w[18] != '-' || w[23] != '-' {
		return false
	}
	if w[14] != '4' {
		return false
	}
	switch w[19] {
	case '8', '9', 'a', 'b', 'A', 'B':
	default:
		return false
	}
	for i, c := range w[:36] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHex(c) {
			return false
		}
	}
	return true
}

func isIDNamedKey(key string) bool {
	lowered := strings.ToLower(key)
	for _, needle := range idFieldNeedles {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

// isEmptyValue treats null, "", [] and {} as absent, so a tool schema that
// merely DECLARES a session_id property does not report as volatile content.
func isEmptyValue(v gjson.Result) bool {
	switch {
	case v.Type == gjson.Null, !v.Exists():
		return true
	case v.Type == gjson.String:
		return v.String() == ""
	case v.IsArray():
		return len(v.Array()) == 0
	case v.IsObject():
		empty := true
		v.ForEach(func(gjson.Result, gjson.Result) bool { empty = false; return false })
		return empty
	}
	return false
}

func sampleOf(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	return v.Raw
}

// truncate caps a sample at SampleBytes without splitting a rune: a partial
// rune would be invalid UTF-8 in the log line.
func truncate(s string) string {
	if len(s) <= SampleBytes {
		return s
	}
	cut := SampleBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
