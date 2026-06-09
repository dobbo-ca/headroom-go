// Package detect classifies a content block into one of seven ContentType
// variants using cheap ordered heuristics. Order matters: the first matching
// rule wins. This is the legacy regex detector; the Magika ML tier is a
// follow-up.
package detect

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DetectionResult is the detector's verdict plus a rough confidence in [0,1].
type DetectionResult struct {
	Type       transform.ContentType
	Confidence float32
}

// These regexes are deliberately cheap heuristics, not exact classifiers. Known
// fuzziness (tracked for refinement against upstream's signal detector): diffRe
// fires on `--- `/`+++ ` lines in changelogs; buildRe matches `error:`/`warning:`
// substrings in prose; searchRe's `[^\d]` tail excludes grep hits whose matched
// text starts with a digit. RE2 has no lookahead, so "`:line:` but not
// `:line:col:`" cannot be expressed cleanly; ordering (search before build) plus
// the `[^\d]` tail covers the common cases. Refining these boundaries is a
// follow-up; do not change the ordering.
var (
	diffRe   = regexp.MustCompile(`(?m)^(diff --git |--- |\+\+\+ |@@ .* @@)`)
	htmlRe   = regexp.MustCompile(`(?i)^\s*<(!doctype|html|head|body|div|span|table|ul|ol|p|a)\b`)
	searchRe = regexp.MustCompile(`(?m)^[^\s:][^:\n]*:\d+:[^\d]`)       // path:line: (not path:line:col:)
	buildRe  = regexp.MustCompile(`(?im)(:\d+:\d+:|error:|warning:|FAILED|panic:|undefined:)`)
	codeRe   = regexp.MustCompile(`(?m)^\s*(package |import |func |class |def |fn |public |private |const |let |var |#include)\b`)
)

// DetectContentType returns the best-guess ContentType for content.
func DetectContentType(content string) DetectionResult {
	s := strings.TrimSpace(content)
	if s == "" {
		return DetectionResult{Type: transform.PlainText, Confidence: 1}
	}
	// 1. JSON array (parse-validated to avoid false positives).
	if strings.HasPrefix(s, "[") && looksLikeJSONArray(s) {
		return DetectionResult{Type: transform.JsonArray, Confidence: 0.95}
	}
	// 2. Diff (git or unified).
	if diffRe.MatchString(s) {
		return DetectionResult{Type: transform.GitDiff, Confidence: 0.9}
	}
	// 3. HTML.
	if htmlRe.MatchString(s) {
		return DetectionResult{Type: transform.Html, Confidence: 0.85}
	}
	// 4. Search results (grep-style path:line:).
	if searchRe.MatchString(s) {
		return DetectionResult{Type: transform.SearchResults, Confidence: 0.8}
	}
	// 5. Build output (compiler diagnostics / failures).
	if buildRe.MatchString(s) {
		return DetectionResult{Type: transform.BuildOutput, Confidence: 0.75}
	}
	// 6. Source code (language keyword at line start).
	if codeRe.MatchString(s) {
		return DetectionResult{Type: transform.SourceCode, Confidence: 0.7}
	}
	// 7. Fallback.
	return DetectionResult{Type: transform.PlainText, Confidence: 0.5}
}

func looksLikeJSONArray(s string) bool {
	var v []json.RawMessage
	return json.Unmarshal([]byte(s), &v) == nil
}
