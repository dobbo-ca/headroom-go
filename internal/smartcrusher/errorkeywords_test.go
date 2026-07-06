package smartcrusher

import (
	"strings"
	"testing"
)

// TestErrorKeywords pins the exact 12-keyword list in order. The rustdoc inside
// constraints.rs WRONGLY lists 11 (omitting "failure"); 12 is authoritative
// [ref: error_keywords.rs].
func TestErrorKeywords(t *testing.T) {
	want := []string{
		"error", "exception", "failed", "failure", "critical", "fatal",
		"crash", "panic", "abort", "timeout", "denied", "rejected",
	}

	if len(ErrorKeywords) != 12 {
		t.Fatalf("len(ErrorKeywords): want 12, got %d", len(ErrorKeywords))
	}
	for i, kw := range want {
		if ErrorKeywords[i] != kw {
			t.Errorf("ErrorKeywords[%d]: want %q, got %q", i, kw, ErrorKeywords[i])
		}
	}
}

// TestErrorKeywordsLowercase asserts the all-lowercase invariant; the caller
// lowercases the haystack, so the keyword list must never be uppercased.
func TestErrorKeywordsLowercase(t *testing.T) {
	for _, kw := range ErrorKeywords {
		if kw != strings.ToLower(kw) {
			t.Errorf("keyword %q is not all-lowercase", kw)
		}
	}
}
