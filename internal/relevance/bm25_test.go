package relevance

import "testing"

func TestBM25MatchesAndClamps(t *testing.T) {
	s := NewBM25Scorer()
	r := s.Score("the quick brown fox", "quick fox")
	if r.Score <= 0 || r.Score > 1 {
		t.Fatalf("score out of (0,1]: %v", r.Score)
	}
	if len(r.MatchedTerms) != 2 { // "fox","quick" (sorted)
		t.Fatalf("matched = %v, want fox+quick", r.MatchedTerms)
	}
	if r.MatchedTerms[0] != "fox" || r.MatchedTerms[1] != "quick" {
		t.Fatalf("matched not sorted: %v", r.MatchedTerms)
	}
}

func TestBM25NoMatch(t *testing.T) {
	s := NewBM25Scorer()
	r := s.Score("alpha beta", "gamma")
	if r.Score != 0 || r.Reason != "BM25: no term matches" {
		t.Fatalf("got %+v", r)
	}
}

func TestBM25LongTokenBonusUsesByteLen(t *testing.T) {
	s := NewBM25Scorer()
	// a matched token of byte-len >= 8 triggers +0.3
	r := s.Score("authentication subsystem", "authentication")
	if r.Score < 0.3 {
		t.Fatalf("expected long-token bonus, got %v", r.Score)
	}
}

func TestBM25ReasonSingleMatch(t *testing.T) {
	s := NewBM25Scorer()
	r := s.Score("the quick brown fox", "quick")
	if r.Reason != "BM25: matched 'quick'" {
		t.Fatalf("single-match reason = %q", r.Reason)
	}
}

func TestBM25ReasonMultiMatchPreviewAndSuffix(t *testing.T) {
	s := NewBM25Scorer()
	// 4 matched terms -> "..." suffix, preview first 3 (sorted: aa, bb, cc, dd)
	r := s.Score("aa bb cc dd", "aa bb cc dd")
	if r.Reason != "BM25: matched 4 terms (aa, bb, cc...)" {
		t.Fatalf("multi-match reason = %q", r.Reason)
	}
}

func TestBM25ReasonMultiMatchNoSuffix(t *testing.T) {
	s := NewBM25Scorer()
	// exactly 2 matched terms -> no "..." suffix
	r := s.Score("aa bb", "aa bb")
	if r.Reason != "BM25: matched 2 terms (aa, bb)" {
		t.Fatalf("multi-match reason = %q", r.Reason)
	}
}

func TestBM25TokenizerUUID(t *testing.T) {
	s := NewBM25Scorer()
	uuid := "550E8400-E29B-41D4-A716-446655440000" // uppercase -> lowercased to match
	r := s.Score("ref "+uuid, uuid)
	if len(r.MatchedTerms) != 1 {
		t.Fatalf("uuid should tokenize whole: matched = %v", r.MatchedTerms)
	}
	if r.MatchedTerms[0] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("uuid token = %q", r.MatchedTerms[0])
	}
}

func TestBM25ScoreBatchEmptyContext(t *testing.T) {
	s := NewBM25Scorer()
	got := s.ScoreBatch([]string{"a", "b"}, "")
	if len(got) != 2 {
		t.Fatalf("batch len = %d", len(got))
	}
	for _, r := range got {
		if r.Score != 0 || r.Reason != "BM25: empty context" {
			t.Fatalf("empty-context item = %+v", r)
		}
	}
}

func TestBM25ScoreBatchReason(t *testing.T) {
	s := NewBM25Scorer()
	got := s.ScoreBatch([]string{"quick fox", "nothing here"}, "quick fox")
	if got[0].Reason != "BM25: 2 terms" {
		t.Fatalf("batch reason[0] = %q", got[0].Reason)
	}
	if got[1].Reason != "BM25: no matches" {
		t.Fatalf("batch reason[1] = %q", got[1].Reason)
	}
}

func TestBM25IsAvailable(t *testing.T) {
	if !NewBM25Scorer().IsAvailable() {
		t.Fatal("bm25 is always available")
	}
}

func TestNewScoreClamps(t *testing.T) {
	if NewScore(5.0, "x", nil).Score != 1.0 {
		t.Fatal("score above 1 must clamp to 1")
	}
	if NewScore(-2.0, "x", nil).Score != 0.0 {
		t.Fatal("score below 0 must clamp to 0")
	}
}

var _ Scorer = (*BM25Scorer)(nil)
