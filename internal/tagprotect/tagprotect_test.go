package tagprotect

import "testing"

func TestProtectRestoreRoundTripCustomBlock(t *testing.T) {
	in := `pre <thinking>secret plan</thinking> post`
	prot, blocks, stats := ProtectTags(in, false)
	if stats.CustomBlocksProtected != 1 {
		t.Fatalf("blocks protected = %d", stats.CustomBlocksProtected)
	}
	if prot == in {
		t.Fatal("custom tag should have been replaced by a placeholder")
	}
	if got := RestoreTags(prot, blocks); got != in {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, in)
	}
}

func TestHTMLTagsNotProtected(t *testing.T) {
	in := `<div>hello <span>world</span></div>`
	prot, _, stats := ProtectTags(in, false)
	if prot != in || stats.HTMLTagsSkipped == 0 {
		t.Fatalf("HTML5 tags must be emitted verbatim; got %q skipped=%d", prot, stats.HTMLTagsSkipped)
	}
}

func TestCommentsAndDoctypeVerbatim(t *testing.T) {
	in := "<!-- note --><!DOCTYPE html><?xml v?>"
	prot, blocks, _ := ProtectTags(in, false)
	if prot != in || len(blocks) != 0 {
		t.Fatalf("non-tags must pass through, got %q blocks=%v", prot, blocks)
	}
}

func TestCollisionAvoidance(t *testing.T) {
	in := `{{HEADROOM_TAG_0}} <custom>x</custom>`
	_, blocks, stats := ProtectTags(in, false)
	if !stats.PlaceholderCollisionAvoided || len(blocks) != 1 {
		t.Fatalf("expected salted prefix; stats=%+v blocks=%v", stats, blocks)
	}
}

func TestEarlyReturnNoAngleBracket(t *testing.T) {
	in := "plain text with no tags"
	prot, blocks, stats := ProtectTags(in, false)
	if prot != in || blocks != nil || stats != (ProtectStats{}) {
		t.Fatalf("early return expected, got prot=%q blocks=%v stats=%+v", prot, blocks, stats)
	}
}

func TestEmptyInput(t *testing.T) {
	prot, blocks, stats := ProtectTags("", false)
	if prot != "" || blocks != nil || stats != (ProtectStats{}) {
		t.Fatalf("empty input must early-return, got prot=%q blocks=%v stats=%+v", prot, blocks, stats)
	}
}

func TestSelfClosingCustomProtected(t *testing.T) {
	in := `before <widget/> after`
	prot, blocks, stats := ProtectTags(in, false)
	if stats.SelfClosingProtected != 1 || len(blocks) != 1 {
		t.Fatalf("self-closing custom must be protected, stats=%+v blocks=%v", stats, blocks)
	}
	if got := RestoreTags(prot, blocks); got != in {
		t.Fatalf("round-trip mismatch: got %q want %q", got, in)
	}
}

func TestOrphanCloseEmittedVerbatim(t *testing.T) {
	in := `text </lonely> more`
	prot, blocks, stats := ProtectTags(in, false)
	if stats.OrphanCloses != 1 {
		t.Fatalf("orphan close count = %d", stats.OrphanCloses)
	}
	if prot != in || len(blocks) != 0 {
		t.Fatalf("orphan close must be emitted verbatim, got %q blocks=%v", prot, blocks)
	}
}

func TestMarkerModeProtectsOpenAndClose(t *testing.T) {
	in := `pre <thinking>inner content</thinking> post`
	prot, blocks, stats := ProtectTags(in, true)
	// marker-only mode: open marker + close marker => 2 blocks, inner flows through.
	if len(blocks) != 2 {
		t.Fatalf("marker mode expects 2 blocks (open+close), got %d: %v", len(blocks), blocks)
	}
	if stats.CustomBlocksProtected != 0 {
		t.Fatalf("marker mode must not count whole blocks, stats=%+v", stats)
	}
	if !contains(prot, "inner content") {
		t.Fatalf("inner content must flow through in marker mode, got %q", prot)
	}
	if got := RestoreTags(prot, blocks); got != in {
		t.Fatalf("round-trip mismatch: got %q want %q", got, in)
	}
}

func TestNestedBlockCollapsesToOuter(t *testing.T) {
	in := `<outer>a<inner/>b</outer>`
	prot, blocks, stats := ProtectTags(in, false)
	if stats.CustomBlocksProtected != 1 {
		t.Fatalf("nested must collapse to a single outer block, stats=%+v", stats)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (outer subsumes inner), got %d: %v", len(blocks), blocks)
	}
	if got := RestoreTags(prot, blocks); got != in {
		t.Fatalf("round-trip mismatch: got %q want %q", got, in)
	}
}

func TestIsKnownHTMLTagCaseInsensitive(t *testing.T) {
	for _, name := range []string{"div", "DIV", "Span", "template", "svg", "math", "search", "hgroup", "slot", "portal"} {
		if !IsKnownHTMLTag(name) {
			t.Errorf("%q should be a known HTML5 tag", name)
		}
	}
	for _, name := range []string{"thinking", "custom", "widget", "foo"} {
		if IsKnownHTMLTag(name) {
			t.Errorf("%q must NOT be a known HTML5 tag", name)
		}
	}
}

func TestRestoreMissingPlaceholderSkipped(t *testing.T) {
	// A block whose placeholder is absent from the text must be skipped (no injection).
	text := "nothing to restore here"
	blocks := [][2]string{{"{{HEADROOM_TAG_0}}", "<custom>x</custom>"}}
	if got := RestoreTags(text, blocks); got != text {
		t.Fatalf("missing placeholder must be skipped, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
