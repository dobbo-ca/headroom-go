// Package relevance provides explainable, ML-free relevance scorers used by the
// SmartCrusher planning layer to pin keep_indices. It ports upstream headroom's
// relevance subsystem (base + bm25 + hybrid + factory). The scorers are pure:
// no time, no randomness, no Plan-2 dependencies.
package relevance

import "fmt"

// Score is an explainable relevance result. Score is always clamped to [0,1].
type Score struct {
	Score        float64
	Reason       string
	MatchedTerms []string
}

// NewScore builds a Score, clamping s to [0,1] (mirrors upstream
// RelevanceScore::new / Python __post_init__). reason and terms are stored
// as-is.
func NewScore(s float64, reason string, terms []string) Score {
	if s < 0.0 {
		s = 0.0
	} else if s > 1.0 {
		s = 1.0
	}
	return Score{Score: s, Reason: reason, MatchedTerms: terms}
}

// EmptyScore is a zero-score result carrying only a reason.
func EmptyScore(reason string) Score {
	return NewScore(0.0, reason, nil)
}

// Scorer is the relevance scoring contract.
type Scorer interface {
	Score(item, context string) Score
	ScoreBatch(items []string, context string) []Score
	IsAvailable() bool
}

// DefaultBatchScore is the per-item fallback batch implementation (maps Score
// over each item). Mirrors upstream default_batch_score.
func DefaultBatchScore(s Scorer, items []string, context string) []Score {
	out := make([]Score, len(items))
	for i, item := range items {
		out[i] = s.Score(item, context)
	}
	return out
}

// CreateScorer is the scorer factory. tier is lowercased before matching.
// Mirrors upstream mod.rs create_scorer.
func CreateScorer(tier string) (Scorer, error) {
	switch toLowerASCII(tier) {
	case "bm25":
		return NewBM25Scorer(), nil
	case "hybrid":
		return NewHybridScorer(), nil
	case "embedding":
		// EmbeddingScorer is stubbed; is_available() is false in v0.
		return nil, fmt.Errorf("EmbeddingScorer requires the ONNX backend (not yet implemented in Rust)")
	default:
		return nil, fmt.Errorf("Unknown scorer tier: %s. Valid tiers: bm25, embedding, hybrid", tier)
	}
}

// toLowerASCII lowercases ASCII letters. CreateScorer's tier match is
// case-insensitive (upstream .to_lowercase()).
func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
