package compress

import (
	"fmt"
	"strings"
	"testing"
)

func prose(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "Sentence number %d describes a distinct topic %d in some detail.\n", i, i)
	}
	return b.String()
}

// I4. Replay re-sends a stored compression on every later turn, so a
// non-deterministic compressor busts the prompt cache continuously. Identical
// input must give byte-identical output, every time.
func TestTextCrusherIsDeterministic(t *testing.T) {
	in := prose(40)
	first := NewTextCrusher(DefaultTextCrusherConfig()).Compress(in, "topic 7", 0)
	for i := 0; i < 20; i++ {
		got := NewTextCrusher(DefaultTextCrusherConfig()).Compress(in, "topic 7", 0)
		if got.Compressed != first.Compressed {
			t.Fatalf("run %d differed; a non-deterministic compressor cannot go on the replay path", i)
		}
	}
	if first.Compressed == in {
		t.Fatal("nothing was dropped; this test would pass on a passthrough")
	}
}

// EXTRACTIVE. Every emitted line must be a verbatim segment of the input. The
// compressor selects; it must never rewrite, paraphrase, or invent a word.
func TestTextCrusherOutputIsVerbatimInputSegments(t *testing.T) {
	in := prose(40)
	res := NewTextCrusher(DefaultTextCrusherConfig()).Compress(in, "", 0)
	if res.Compressed == in {
		t.Fatal("nothing was dropped; the assertion below would be vacuous")
	}
	segs := map[string]bool{}
	for _, s := range splitSegments(in) {
		segs[s] = true
	}
	for _, line := range strings.Split(res.Compressed, "\n") {
		if !segs[line] {
			t.Errorf("emitted a line that is not a verbatim input segment: %q", line)
		}
	}
}

// Order must be preserved: the kept sentences read in their original sequence,
// not in score order.
func TestTextCrusherPreservesOriginalOrder(t *testing.T) {
	in := prose(40)
	res := NewTextCrusher(DefaultTextCrusherConfig()).Compress(in, "", 0)
	order := splitSegments(in)
	pos := map[string]int{}
	for i, s := range order {
		pos[s] = i
	}
	last := -1
	for _, line := range strings.Split(res.Compressed, "\n") {
		p, ok := pos[line]
		if !ok {
			continue
		}
		if p < last {
			t.Fatalf("segment %q came out before an earlier one; order was not preserved", line)
		}
		last = p
	}
}

// Below the segment floor there is nothing to gain, so the input comes back
// untouched rather than mangled.
func TestTextCrusherPassesThroughShortInput(t *testing.T) {
	in := "One sentence. Two sentences. Three."
	res := NewTextCrusher(DefaultTextCrusherConfig()).Compress(in, "", 0)
	if res.Compressed != in {
		t.Errorf("short input was altered:\n got %q\nwant %q", res.Compressed, in)
	}
	if res.CompressionRatio != 1.0 {
		t.Errorf("ratio = %v, want 1.0", res.CompressionRatio)
	}
}

// Near-duplicate suppression: a document that repeats itself must not have the
// repeats selected, so it compresses much harder than a document of distinct
// sentences.
func TestTextCrusherSuppressesNearDuplicates(t *testing.T) {
	distinct := prose(30)
	repeated := strings.Repeat("The build failed while linking the shared object.\n", 30)

	c := NewTextCrusher(DefaultTextCrusherConfig())
	d := c.Compress(distinct, "", 0)
	r := c.Compress(repeated, "", 0)

	if r.KeptSegments >= d.KeptSegments {
		t.Errorf("kept %d of %d repeated segments but only %d of %d distinct ones; "+
			"near-duplicate suppression is not firing",
			r.KeptSegments, r.TotalSegments, d.KeptSegments, d.TotalSegments)
	}
}

// Relevance must actually bias selection, or the BM25 term is dead weight.
func TestTextCrusherRelevanceBiasesSelection(t *testing.T) {
	// The matching sentence goes FIRST, where recency is lowest. Only the
	// relevance term can rescue it, so a build that ignores relevance drops it.
	var b strings.Builder
	b.WriteString("The deployment pipeline uses kubernetes and helm charts.\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "Paragraph %d covers ordinary unremarkable material.\n", i)
	}
	in := b.String()

	c := NewTextCrusher(DefaultTextCrusherConfig())

	// CONTROL: with no query it is dropped, so the assertion below is about
	// relevance rather than about the sentence being kept anyway.
	if noQuery := c.Compress(in, "", 0); strings.Contains(noQuery.Compressed, "kubernetes and helm") {
		t.Skip("fixture no longer drops the target sentence without a query")
	}

	withQuery := c.Compress(in, "kubernetes helm", 0)
	if !strings.Contains(withQuery.Compressed, "kubernetes and helm") {
		t.Errorf("the segment matching the query was dropped:\n%s", withQuery.Compressed)
	}
}

func TestIsSalient(t *testing.T) {
	tests := []struct {
		word string
		want bool
	}{
		{"error", true}, {"FAILED", true}, {"traceback", true},
		{"HTTP", true}, {"os.path", true}, {"line42", true},
		{"the", false}, {"ordinary", false}, {"a", false},
	}
	for _, tt := range tests {
		if got := isSalient(tt.word); got != tt.want {
			t.Errorf("isSalient(%q) = %v, want %v", tt.word, got, tt.want)
		}
	}
}

func TestSplitSegments(t *testing.T) {
	got := splitSegments("One. Two!\n\n  Three?  Four\n")
	want := []string{"One.", "Two!", "Three?", "Four"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The index tiebreak is what makes selection a total order, and a total order
// is what makes the output deterministic — which replay requires (I4).
//
// With the default weights it is nearly unreachable: recency is (i+1)/n, so it
// strictly increases with index and two segments almost never tie. Zeroing the
// recency weight makes ties the common case, which is the only way to put the
// tiebreak under test rather than assert a branch nothing reaches.
func TestTiebreakPrefersTheEarlierSegment(t *testing.T) {
	cfg := DefaultTextCrusherConfig()
	cfg.WRecency = 0 // remove the term that separates otherwise identical segments
	cfg.TargetRatio = 0.25

	// Same length, same salience, no query: these tie on score, and only the
	// index tiebreak can order them.
	// Every word carries the index, so no two segments share a shingle and
	// near-duplicate suppression cannot decide the outcome instead.
	var b strings.Builder
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "alpha%02d bravo%02d charlie%02d delta%02d echo%02d foxtrot%02d.\n", i, i, i, i, i, i)
	}
	res := NewTextCrusher(cfg).Compress(b.String(), "", 0)
	if res.KeptSegments == res.TotalSegments {
		t.Fatal("nothing was dropped; the tiebreak never had to choose")
	}

	kept := strings.Split(res.Compressed, "\n")
	if len(kept) < 2 {
		t.Fatalf("kept %d segments, need at least 2 to see an ordering", len(kept))
	}
	// Ties must resolve to the LOWEST indices, so the first kept segment is
	// alpha00. A flipped tiebreak keeps the last ones instead.
	if !strings.HasPrefix(kept[0], "alpha00") {
		t.Errorf("first kept segment is %q, want alpha00: ties did not resolve to the earliest index", kept[0])
	}
}

// Determinism must hold under the tie-heavy config too, where the comparator
// leans on the tiebreak rather than on distinct scores.
func TestTextCrusherIsDeterministicUnderTies(t *testing.T) {
	cfg := DefaultTextCrusherConfig()
	cfg.WRecency = 0
	cfg.TargetRatio = 0.25
	var b strings.Builder
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "alpha%02d bravo%02d charlie%02d delta%02d echo%02d foxtrot%02d.\n", i, i, i, i, i, i)
	}
	in := b.String()

	first := NewTextCrusher(cfg).Compress(in, "", 0).Compressed
	for i := 0; i < 20; i++ {
		if got := NewTextCrusher(cfg).Compress(in, "", 0).Compressed; got != first {
			t.Fatalf("run %d differed under ties", i)
		}
	}
}
