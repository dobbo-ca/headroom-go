package relevance

import (
	"fmt"
	"regexp"
	"strings"
)

// compute_alpha patterns. uuidPattern and numericIDPattern scan the ORIGINAL
// context; hostnamePattern and emailPattern scan the LOWERCASED context.
var (
	uuidPattern      = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	numericIDPattern = regexp.MustCompile(`\b\d{4,}\b`)
	hostnamePattern  = regexp.MustCompile(`\b[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9]*(?:\.[a-zA-Z]{2,})?\b`)
	// emailPattern preserves the upstream literal-pipe quirk: `[A-Z|a-z]` makes
	// '|' a valid TLD char. Mirrored verbatim for parity — do NOT "fix" it.
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`)
)

// embeddingScorer is the stubbed semantic ONNX scorer. is_available() is false
// in v0, so it is never reached on the hybrid path.
type embeddingScorer struct{}

func (embeddingScorer) IsAvailable() bool { return false }

// HybridScorer fuses BM25 with an (unavailable) embedding scorer using an
// adaptive per-query alpha. In v0 the embedding scorer is unavailable, so it
// always runs the BM25-only boosted fallback. Ports upstream HybridScorer.
type HybridScorer struct {
	baseAlpha          float64
	adaptive           bool
	bm25               *BM25Scorer
	embedding          embeddingScorer
	embeddingAvailable bool
}

// NewHybridScorer returns a HybridScorer with upstream defaults
// (base_alpha=0.5, adaptive=true) and a stubbed embedding scorer.
func NewHybridScorer() *HybridScorer {
	emb := embeddingScorer{}
	return &HybridScorer{
		baseAlpha:          0.5,
		adaptive:           true,
		bm25:               NewBM25Scorer(),
		embedding:          emb,
		embeddingAvailable: emb.IsAvailable(),
	}
}

// IsAvailable always returns true (it can always fall back to BM25).
func (h *HybridScorer) IsAvailable() bool { return true }

// computeAlpha computes the adaptive BM25 weight from context signals. Only ever
// used on the future fused path (unreachable in v0). Returns base_alpha
// unclamped when !adaptive.
func (h *HybridScorer) computeAlpha(context string) float64 {
	if !h.adaptive {
		return h.baseAlpha
	}
	contextLower := strings.ToLower(context)
	uuidCount := len(uuidPattern.FindAllString(context, -1))
	idCount := len(numericIDPattern.FindAllString(context, -1))
	hostnameCount := len(hostnamePattern.FindAllString(contextLower, -1))
	emailCount := len(emailPattern.FindAllString(contextLower, -1))

	alpha := h.baseAlpha
	switch {
	case uuidCount > 0:
		alpha = max64(alpha, 0.85)
	case idCount >= 2:
		alpha = max64(alpha, 0.75)
	case idCount == 1:
		alpha = max64(alpha, 0.65)
	case hostnameCount > 0 || emailCount > 0:
		alpha = max64(alpha, 0.6)
	}
	return clamp64(alpha, 0.3, 0.9)
}

// boostBM25Only is the v0 fallback: boost a BM25 result when terms matched.
func (h *HybridScorer) boostBM25Only(r Score) Score {
	boosted := r.Score
	if len(r.MatchedTerms) > 0 {
		boosted = max64(boosted, 0.3)
		if len(r.MatchedTerms) >= 2 {
			boosted += 0.2
			if boosted > 1.0 {
				boosted = 1.0
			}
		}
	}
	return NewScore(boosted, "Hybrid (BM25 only, boosted): "+r.Reason, r.MatchedTerms)
}

// Score scores a single item. In v0 it always takes the boosted BM25-only path.
func (h *HybridScorer) Score(item, context string) Score {
	bm25Result := h.bm25.Score(item, context)
	if !h.embeddingAvailable {
		return h.boostBM25Only(bm25Result)
	}
	// Future fused path (unreachable in v0).
	emb := h.embedding
	_ = emb
	alpha := h.computeAlpha(context)
	combined := alpha*bm25Result.Score + (1-alpha)*0.0
	reason := fmt.Sprintf("Hybrid (α=%.2f): BM25=%.2f, Semantic=%.2f", alpha, bm25Result.Score, 0.0)
	return NewScore(combined, reason, bm25Result.MatchedTerms)
}

// ScoreBatch scores items against a shared context. In v0 it boosts each BM25
// batch result.
func (h *HybridScorer) ScoreBatch(items []string, context string) []Score {
	if len(items) == 0 {
		return nil
	}
	bm25Results := h.bm25.ScoreBatch(items, context)
	if !h.embeddingAvailable {
		out := make([]Score, len(bm25Results))
		for i, r := range bm25Results {
			out[i] = h.boostBM25Only(r)
		}
		return out
	}
	// Future fused path (unreachable in v0).
	alpha := h.computeAlpha(context)
	out := make([]Score, len(bm25Results))
	for i, r := range bm25Results {
		combined := alpha*r.Score + (1-alpha)*0.0
		reason := fmt.Sprintf("Hybrid (α=%.2f): BM25=%.2f, Emb=%.2f", alpha, r.Score, 0.0)
		out[i] = NewScore(combined, reason, r.MatchedTerms)
	}
	return out
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func clamp64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
