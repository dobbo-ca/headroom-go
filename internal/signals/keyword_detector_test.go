package signals

import "testing"

func TestKeywordDetectorCategories(t *testing.T) {
	d := NewKeywordDetector()
	cases := []struct {
		line string
		ctx  ImportanceContext
		cat  ImportanceCategory
		prio float32
	}{
		{"FATAL: disk full", Text, Error, 0.95},
		{"a warning about config", Log, Warning, 0.75},
		{"auth token rotated", Diff, Security, 0.85}, // Security only fires in Diff
		{"added password = secret", Diff, Security, 0.85},
		{"TODO: refactor", Search, Importance, 0.6},
		{"# Heading", Text, Markdown, 0.45},
		{"nothing here", Text, 0, 0}, // no match
	}
	for _, c := range cases {
		got := d.Score(c.line, c.ctx)
		if c.prio == 0 {
			if got.IsMatch() {
				t.Errorf("%q: expected no match, got %+v", c.line, got)
			}
			continue
		}
		if !got.IsMatch() || *got.Category != c.cat || got.Priority != c.prio || got.Confidence != 0.7 {
			t.Errorf("%q ctx=%v: got %+v, want cat=%v prio=%v", c.line, c.ctx, got, c.cat, c.prio)
		}
	}
}

func TestWarningGatedOutInDiff(t *testing.T) {
	d := NewKeywordDetector()
	// "warning" is excluded in Diff context and there is no other keyword here.
	if d.Score("warning suppressed in diff", Diff).IsMatch() {
		t.Error("Warning keywords must be suppressed in Diff context")
	}
}

func TestSecurityOnlyInDiff(t *testing.T) {
	d := NewKeywordDetector()
	// Security keywords must NOT fire outside Diff.
	if got := d.Score("password rotation policy", Text); got.IsMatch() && *got.Category == Security {
		t.Error("Security must only fire in Diff context")
	}
}

func TestHighestPriorityWins(t *testing.T) {
	d := NewKeywordDetector()
	// "error" (Error 0.95) and "todo" (Importance 0.6) both present; Error wins.
	got := d.Score("TODO: fix the error", Text)
	if !got.IsMatch() || *got.Category != Error || got.Priority != 0.95 {
		t.Fatalf("expected Error to win by priority, got %+v", got)
	}
}

func TestWordBoundary(t *testing.T) {
	d := NewKeywordDetector()
	if d.Score("failover complete", Text).IsMatch() {
		t.Error("'failover' must not match 'fail' (word boundary)")
	}
}

func TestLongestMatchAtPosition(t *testing.T) {
	d := NewKeywordDetector()
	// "warning" must match as a whole (warn is a prefix); category is Warning either way.
	got := d.Score("warning issued", Text)
	if !got.IsMatch() || *got.Category != Warning {
		t.Fatalf("expected Warning, got %+v", got)
	}
}

func TestContainsErrorIndicator(t *testing.T) {
	d := NewKeywordDetector()
	if !d.ContainsErrorIndicator("Traceback (most recent call last)") {
		t.Error("substring 'traceback' should match")
	}
	if d.ContainsErrorIndicator("abort retry") { // 'abort' is NOT in the indicator list
		t.Error("'abort' is not an error indicator")
	}
	// Substring-only: 'failover' contains 'fail' so it matches (no word boundary).
	if !d.ContainsErrorIndicator("failover complete") {
		t.Error("substring 'fail' inside 'failover' should match (no boundary)")
	}
}

var _ LineImportanceDetector = (*KeywordDetector)(nil)
