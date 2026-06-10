package relevance

import (
	"strings"
	"testing"
)

func TestHybridV0AlwaysBoostsBM25(t *testing.T) {
	h := NewHybridScorer()
	if !h.IsAvailable() {
		t.Fatal("hybrid is always available")
	}
	r := h.Score("the quick brown fox", "quick fox")
	// two matched terms -> floor 0.3 then +0.2
	if r.Score < 0.5 {
		t.Fatalf("expected boosted >=0.5, got %v", r.Score)
	}
	if r.Reason[:6] != "Hybrid" {
		t.Fatalf("reason = %q", r.Reason)
	}
}

func TestHybridFallbackReasonWrapsBM25(t *testing.T) {
	h := NewHybridScorer()
	r := h.Score("the quick brown fox", "quick")
	if !strings.HasPrefix(r.Reason, "Hybrid (BM25 only, boosted): BM25: matched 'quick'") {
		t.Fatalf("reason = %q", r.Reason)
	}
}

func TestHybridNoMatchStaysZero(t *testing.T) {
	h := NewHybridScorer()
	r := h.Score("alpha beta", "gamma")
	if r.Score != 0.0 {
		t.Fatalf("no-match must stay zero, got %v", r.Score)
	}
}

func TestHybridSingleMatchFloorNoMultiBonus(t *testing.T) {
	h := NewHybridScorer()
	// one matched short term -> floor 0.3, no +0.2 (needs >=2 terms)
	r := h.Score("the quick brown fox", "quick")
	if r.Score != 0.3 {
		t.Fatalf("single-match floor should be exactly 0.3, got %v", r.Score)
	}
}

func TestHybridScoreBatchBoosts(t *testing.T) {
	h := NewHybridScorer()
	got := h.ScoreBatch([]string{"quick fox", "nothing here"}, "quick fox")
	if len(got) != 2 {
		t.Fatalf("batch len = %d", len(got))
	}
	if !strings.HasPrefix(got[0].Reason, "Hybrid (BM25 only, boosted):") {
		t.Fatalf("batch reason[0] = %q", got[0].Reason)
	}
	if got[1].Score != 0.0 {
		t.Fatalf("no-match batch item must stay 0, got %v", got[1].Score)
	}
}

func TestHybridScoreBatchEmptyItems(t *testing.T) {
	h := NewHybridScorer()
	if got := h.ScoreBatch(nil, "quick fox"); len(got) != 0 {
		t.Fatalf("empty items -> empty result, got %v", got)
	}
}

func TestCreateScorerFactory(t *testing.T) {
	if s, err := CreateScorer("BM25"); err != nil || s == nil {
		t.Fatalf("bm25 tier: %v", err)
	}
	if s, err := CreateScorer("Hybrid"); err != nil || s == nil {
		t.Fatalf("hybrid tier: %v", err)
	}
	if _, err := CreateScorer("embedding"); err == nil ||
		err.Error() != "EmbeddingScorer requires the ONNX backend (not yet implemented in Rust)" {
		t.Fatalf("embedding tier err = %v", err)
	}
	if _, err := CreateScorer("bogus"); err == nil ||
		err.Error() != "Unknown scorer tier: bogus. Valid tiers: bm25, embedding, hybrid" {
		t.Fatalf("unknown tier err = %v", err)
	}
}

var _ Scorer = (*HybridScorer)(nil)
