package relevance

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// tokenPattern is the BM25 tokenizer: a single regex with 3 alternatives in a
// load-bearing order — whole UUID first, then a 4+ digit numeric id, then a
// generic alnum/underscore word. Applied to the LOWERCASED text. Go's default
// regexp engine is leftmost-first within an alternation (Perl semantics), which
// matches upstream Rust `regex` / Python `re` — do NOT use regexp.CompilePOSIX.
var tokenPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\b\d{4,}\b|[a-zA-Z0-9_]+`)

// idf is the fixed single-doc simplified IDF constant: ln(2). NOT ln(N/df).
var idf = math.Log(2)

// BM25Scorer is a pure-keyword TF-IDF + length-normalization scorer (zero ML
// deps). Ports upstream BM25Scorer.
type BM25Scorer struct {
	k1            float64
	b             float64
	normalizeFlag bool
	maxScore      float64
}

// NewBM25Scorer returns a BM25Scorer with upstream defaults
// (k1=1.5, b=0.75, normalize=true, max_score=10.0).
func NewBM25Scorer() *BM25Scorer {
	return &BM25Scorer{k1: 1.5, b: 0.75, normalizeFlag: true, maxScore: 10.0}
}

// IsAvailable always returns true (no external backend).
func (s *BM25Scorer) IsAvailable() bool { return true }

// tokenize lowercases the text, then returns every TOKEN_PATTERN match.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	return tokenPattern.FindAllString(lower, -1)
}

// freqMap counts token occurrences.
func freqMap(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// bm25Score scores docTokens against queryFreq. avgDocLen<=0 means "single-doc"
// mode where avgdl collapses to docLen (neutral length-norm). Returns the raw
// score and the matched query terms (sorted alphabetically).
func (s *BM25Scorer) bm25Score(docTokens []string, queryFreq map[string]int, avgDocLen float64) (float64, []string) {
	if len(docTokens) == 0 || len(queryFreq) == 0 {
		return 0.0, nil
	}
	docFreq := freqMap(docTokens)
	docLen := float64(len(docTokens))

	var avgdl float64
	if avgDocLen > 0.0 {
		avgdl = avgDocLen
	} else if docLen > 0.0 {
		avgdl = docLen
	} else {
		avgdl = 1.0
	}

	// Iterate query terms in sorted order for determinism.
	keys := make([]string, 0, len(queryFreq))
	for term := range queryFreq {
		keys = append(keys, term)
	}
	sort.Strings(keys)

	var score float64
	var matched []string
	for _, term := range keys {
		f, ok := docFreq[term]
		if !ok {
			continue
		}
		qf := queryFreq[term]
		ff := float64(f)
		numerator := ff * (s.k1 + 1.0)
		denominator := ff + s.k1*(1.0-s.b+s.b*docLen/avgdl)
		termScore := idf * numerator / denominator
		score += termScore * float64(qf)
		matched = append(matched, term)
	}
	return score, matched
}

// finalize applies normalization and the long-token bonus to a raw score.
func (s *BM25Scorer) finalize(raw float64, matched []string) float64 {
	normalized := raw
	if s.normalizeFlag {
		normalized = raw / s.maxScore
		if normalized > 1.0 {
			normalized = 1.0
		}
	}
	// Long-token bonus: any matched term with BYTE length >= 8 -> +0.3.
	for _, t := range matched {
		if len(t) >= 8 {
			normalized += 0.3
			if normalized > 1.0 {
				normalized = 1.0
			}
			break
		}
	}
	return normalized
}

// Score scores a single item against the context.
func (s *BM25Scorer) Score(item, context string) Score {
	itemTokens := tokenize(item)
	contextTokens := tokenize(context)
	queryFreq := freqMap(contextTokens)

	raw, matched := s.bm25Score(itemTokens, queryFreq, 0.0)
	normalized := s.finalize(raw, matched)

	n := len(matched)
	var reason string
	switch {
	case n == 0:
		reason = "BM25: no term matches"
	case n == 1:
		reason = fmt.Sprintf("BM25: matched '%s'", matched[0])
	default:
		preview := matched
		if len(preview) > 3 {
			preview = preview[:3]
		}
		suffix := ""
		if n > 3 {
			suffix = "..."
		}
		reason = fmt.Sprintf("BM25: matched %d terms (%s%s)", n, strings.Join(preview, ", "), suffix)
	}

	matchedCapped := matched
	if len(matchedCapped) > 10 {
		matchedCapped = matchedCapped[:10]
	}
	return NewScore(normalized, reason, matchedCapped)
}

// ScoreBatch scores items against a shared context, using the batch-average doc
// length for length-normalization.
func (s *BM25Scorer) ScoreBatch(items []string, context string) []Score {
	contextTokens := tokenize(context)
	if len(contextTokens) == 0 {
		out := make([]Score, len(items))
		for i := range items {
			out[i] = EmptyScore("BM25: empty context")
		}
		return out
	}
	queryFreq := freqMap(contextTokens)

	itemTokens := make([][]string, len(items))
	totalLen := 0
	for i, item := range items {
		itemTokens[i] = tokenize(item)
		totalLen += len(itemTokens[i])
	}
	denom := len(items)
	if denom < 1 {
		denom = 1
	}
	avgLen := float64(totalLen) / float64(denom)

	out := make([]Score, len(items))
	for i, tokens := range itemTokens {
		raw, matched := s.bm25Score(tokens, queryFreq, avgLen)
		normalized := s.finalize(raw, matched)

		n := len(matched)
		var reason string
		if n == 0 {
			reason = "BM25: no matches"
		} else {
			reason = fmt.Sprintf("BM25: %d terms", n)
		}

		matchedCapped := matched
		if len(matchedCapped) > 5 {
			matchedCapped = matchedCapped[:5]
		}
		out[i] = NewScore(normalized, reason, matchedCapped)
	}
	return out
}
