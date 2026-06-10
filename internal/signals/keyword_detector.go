package signals

import "strings"

// Keyword lists per category (verbatim from upstream). Membership is what
// matters for behavior; the order is cosmetic.
//
//   - errorKeywords + importanceKeywords form the "universal" set (all contexts).
//   - warningKeywords fire only in Search, Log, Text (NOT Diff).
//   - securityKeywords fire only in Diff. ('token' is deliberately excluded.)
//   - markdownPrefixes are line-prefix matches in Text only (no word boundary).
var (
	errorKeywords = []string{
		"error", "exception", "fail", "failed", "failure", "fatal",
		"critical", "crash", "panic", "abort", "timeout", "denied", "rejected",
	}
	importanceKeywords = []string{
		"important", "note", "todo", "fixme", "hack", "xxx", "bug", "fix",
	}
	warningKeywords  = []string{"warn", "warning"}
	securityKeywords = []string{"security", "auth", "password", "secret"}

	markdownPrefixes = []string{"# ", "## ", "### ", "#### ", "**", "> "}

	// errorIndicators is the substring-only (no boundary) triage set. It
	// includes "traceback" and omits abort/timeout/denied/rejected, so it is
	// distinct from the full error word-boundary set.
	errorIndicators = []string{
		"error", "fail", "exception", "traceback", "fatal", "panic", "crash",
	}
)

// keywordEntry pairs a lowercased keyword with its category.
type keywordEntry struct {
	word string
	cat  ImportanceCategory
}

// KeywordDetector is a concrete LineImportanceDetector built from the default
// keyword set. It classifies a line into the single highest-priority category
// allowed in the line's context, emitting a fixed confidence of 0.7 on any
// match.
type KeywordDetector struct {
	universal []keywordEntry // Error + Importance (all contexts)
	warning   []keywordEntry // Warning (Search, Log, Text)
	security  []keywordEntry // Security (Diff)
}

// NewKeywordDetector wires the default keyword set.
func NewKeywordDetector() *KeywordDetector {
	d := &KeywordDetector{}
	for _, w := range errorKeywords {
		d.universal = append(d.universal, keywordEntry{w, Error})
	}
	for _, w := range importanceKeywords {
		d.universal = append(d.universal, keywordEntry{w, Importance})
	}
	for _, w := range warningKeywords {
		d.warning = append(d.warning, keywordEntry{w, Warning})
	}
	for _, w := range securityKeywords {
		d.security = append(d.security, keywordEntry{w, Security})
	}
	return d
}

// wordByte reports whether b is a word byte ([A-Za-z0-9_]).
func wordByte(b byte) bool {
	lb := b | 0x20
	return (lb >= 'a' && lb <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// lowerByte ASCII-lowercases a single byte.
func lowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 0x20
	}
	return b
}

// matchesWordAt reports whether the lowercased keyword kw occurs in line at
// position i with valid word boundaries. line is matched case-insensitively
// (ASCII). A match at [i, i+len(kw)) is valid iff the byte before i and the byte
// after the match end are not word bytes (string edges count as valid).
func matchesWordAt(line string, i int, kw string) bool {
	end := i + len(kw)
	if end > len(line) {
		return false
	}
	// Left boundary.
	if i > 0 && wordByte(line[i-1]) {
		return false
	}
	// Right boundary.
	if end < len(line) && wordByte(line[end]) {
		return false
	}
	// Case-insensitive byte compare.
	for k := 0; k < len(kw); k++ {
		if lowerByte(line[i+k]) != kw[k] {
			return false
		}
	}
	return true
}

// Score returns the single highest-priority category that is allowed in ctx, or
// Neutral() if nothing matched. Confidence is 0.7 on any match. Selection is
// max-by-priority across all matching, context-allowed categories (not a sum,
// not first-found).
func (d *KeywordDetector) Score(line string, ctx ImportanceContext) ImportanceSignal {
	best := Neutral()

	consider := func(cat ImportanceCategory) {
		p := priorityFor(cat)
		if !best.IsMatch() || p > best.Priority {
			best = Matched(cat, p, keywordConfidence)
		}
	}

	// Build the set of word-boundary automatons allowed in this context.
	// Universal (Error + Importance) fires in all contexts.
	// Warning fires only in Search, Log, Text (NOT Diff).
	// Security fires only in Diff.
	var entries []keywordEntry
	entries = append(entries, d.universal...)
	if ctx != Diff {
		entries = append(entries, d.warning...)
	}
	if ctx == Diff {
		entries = append(entries, d.security...)
	}

	// Single scan over the line: for each position, take any matching entry's
	// category and keep the highest-priority one.
	for i := 0; i < len(line); i++ {
		for _, e := range entries {
			if matchesWordAt(line, i, e.word) {
				consider(e.cat)
			}
		}
	}

	// Markdown fires only in Text, as a line-prefix match (no word boundary).
	if ctx == Text {
		for _, p := range markdownPrefixes {
			if strings.HasPrefix(line, p) {
				consider(Markdown)
				break
			}
		}
	}

	return best
}

// ContainsErrorIndicator is a fast substring-only (NO word boundary) triage
// check against the 7-item error-indicator set. Case-insensitive.
func (d *KeywordDetector) ContainsErrorIndicator(line string) bool {
	lower := strings.ToLower(line)
	for _, ind := range errorIndicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}
