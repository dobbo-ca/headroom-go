package compress

import (
	"sort"
	"strings"
	"unicode"

	"github.com/dobbo-ca/headroom-go/internal/relevance"
)

// TextCrusher is a fast, deterministic, EXTRACTIVE prose compressor: it splits
// text into sentence segments, scores each by recency + query relevance +
// structural salience, suppresses near-duplicates with a word-shingle index,
// and keeps the top segments in their original order up to a character budget.
//
// The output is verbatim input segments, trimmed and re-joined with newlines.
// It selects; it never rewrites, and it never invents a word.
//
// Ported from upstream's crates/headroom-core/src/transforms/text_crusher/.
// Two things make it the right answer for the plain-text bucket rather than a
// language model:
//
//   - It is DETERMINISTIC. Replay re-sends a stored compression on every later
//     turn, so a non-deterministic compressor busts the prompt cache
//     continuously (I4). Fixed weights and a stable tiebreak give identical
//     output for identical input.
//   - It runs in milliseconds. Upstream's own note calls this "the
//     request-path-safe alternative to ModernBERT (kompress): ~milliseconds
//     instead of minutes".
//
// ponytail: ASCII path only. Upstream added an ICU (UAX#29) segmentation path
// for CJK, which has no spaces or ASCII terminators; that needs a segmentation
// library and headroom-go is dependency-light by design. Upstream notes the
// ASCII path is byte-identical to the original algorithm, so this is the whole
// compressor for non-CJK input and a passthrough for CJK. Add the ICU path only
// if CJK shows up in a corpus measurement.
type TextCrusher struct {
	config TextCrusherConfig
	scorer *relevance.BM25Scorer
}

// TextCrusherConfig holds the tuning knobs. Defaults mirror upstream exactly.
type TextCrusherConfig struct {
	// TargetRatio is roughly the fraction of CHARACTERS to keep.
	TargetRatio float64
	WRecency    float64
	WRelevance  float64
	WSalience   float64
	// MinSegmentChars: shorter segments are de-prioritised (x0.25).
	MinSegmentChars int
	// NearDupThreshold: skip a candidate when this fraction of its word
	// shingles is already covered by segments already kept.
	NearDupThreshold float64
	// MinSegmentsForCrush: below this many segments, pass through unchanged.
	MinSegmentsForCrush int
}

// DefaultTextCrusherConfig returns upstream's defaults.
func DefaultTextCrusherConfig() TextCrusherConfig {
	return TextCrusherConfig{
		TargetRatio:         0.5,
		WRecency:            1.0,
		WRelevance:          2.0,
		WSalience:           1.5,
		MinSegmentChars:     12,
		NearDupThreshold:    0.85,
		MinSegmentsForCrush: 6,
	}
}

// TextCrusherResult is one compression's outcome.
type TextCrusherResult struct {
	Compressed       string
	OriginalTokens   int
	CompressedTokens int
	CompressionRatio float64
	KeptSegments     int
	TotalSegments    int
}

// NewTextCrusher builds a TextCrusher with the given config. A zero config
// selects the defaults.
func NewTextCrusher(cfg TextCrusherConfig) *TextCrusher {
	if cfg.TargetRatio == 0 {
		cfg = DefaultTextCrusherConfig()
	}
	return &TextCrusher{config: cfg, scorer: relevance.NewBM25Scorer()}
}

// textCrusherKeywords mark a segment as worth keeping regardless of relevance.
var textCrusherKeywords = map[string]bool{
	"error": true, "exception": true, "failed": true, "failure": true,
	"fail": true, "warning": true, "traceback": true, "assert": true,
	"todo": true, "fixme": true,
}

// Compress selects the segments to keep. context biases relevance; pass "" for
// none. ratio overrides the configured target when > 0.
func (c *TextCrusher) Compress(content, context string, ratio float64) TextCrusherResult {
	cfg := c.config
	if ratio <= 0 {
		ratio = cfg.TargetRatio
	}
	if ratio < 0.05 {
		ratio = 0.05
	}
	if ratio > 1.0 {
		ratio = 1.0
	}

	segments := splitSegments(content)
	if len(segments) < cfg.MinSegmentsForCrush {
		return textCrusherPassthrough(content, len(segments))
	}

	n := len(segments)
	totalChars := 0
	for _, s := range segments {
		totalChars += len(s)
	}
	// max(1) so a tiny input never truncates the budget to zero, which would
	// admit nothing and silently fall back to a 100% passthrough.
	targetChars := int(float64(totalChars) * ratio)
	if targetChars < 1 {
		targetChars = 1
	}

	segTokens := make([][]string, n)
	for i, s := range segments {
		segTokens[i] = crusherTokens(s)
	}

	scored := c.scorer.ScoreBatch(segments, context)
	scores := make([]float64, n)
	for i := range segments {
		recency := (float64(i) + 1.0) / float64(n)
		var rel float64
		if i < len(scored) {
			rel = scored[i].Score
		}
		words := strings.Fields(segments[i])
		salient := 0
		for _, w := range words {
			if isSalient(w) {
				salient++
			}
		}
		salience := float64(salient) / (float64(len(words)) + 1.0)
		s := cfg.WRecency*recency + cfg.WRelevance*rel + cfg.WSalience*salience
		if len(segments[i]) < cfg.MinSegmentChars {
			s *= 0.25
		}
		scores[i] = s
	}

	// Highest score first, with a stable tiebreak on index so the selection is
	// deterministic (I4) regardless of sort implementation.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if scores[ia] != scores[ib] {
			return scores[ia] > scores[ib]
		}
		return ia < ib
	})

	kept := make([]bool, n)
	seen := map[string]bool{}
	keptChars, keptCount := 0, 0
	for _, i := range order {
		if keptChars >= targetChars {
			break
		}
		sh := shingles(segTokens[i], 3)
		if len(sh) > 0 {
			covered := 0
			for s := range sh {
				if seen[s] {
					covered++
				}
			}
			if float64(covered)/float64(len(sh)) >= cfg.NearDupThreshold {
				continue // near-duplicate: most shingles are already kept
			}
		}
		kept[i] = true
		keptCount++
		for s := range sh {
			seen[s] = true
		}
		keptChars += len(segments[i])
	}

	if keptCount == 0 {
		return textCrusherPassthrough(content, n)
	}

	out := make([]string, 0, keptCount)
	for i := 0; i < n; i++ {
		if kept[i] {
			out = append(out, segments[i])
		}
	}
	compressed := strings.Join(out, "\n")

	origTok := countWords(content)
	compTok := countWords(compressed)
	r := 1.0
	if origTok > 0 {
		r = float64(compTok) / float64(origTok)
	}
	return TextCrusherResult{
		Compressed:       compressed,
		OriginalTokens:   origTok,
		CompressedTokens: compTok,
		CompressionRatio: r,
		KeptSegments:     keptCount,
		TotalSegments:    n,
	}
}

func textCrusherPassthrough(content string, nSegments int) TextCrusherResult {
	t := countWords(content)
	return TextCrusherResult{
		Compressed:       content,
		OriginalTokens:   t,
		CompressedTokens: t,
		CompressionRatio: 1.0,
		KeptSegments:     nSegments,
		TotalSegments:    nSegments,
	}
}

func countWords(s string) int { return len(strings.Fields(s)) }

// splitSegments breaks text on newlines, and after a `.`/`!`/`?` followed by
// whitespace. Blank lines are dropped and every segment is trimmed.
func splitSegments(text string) []string {
	var segs []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var cur strings.Builder
		prevTerm := false
		for _, ch := range trimmed {
			if prevTerm && unicode.IsSpace(ch) {
				if s := strings.TrimSpace(cur.String()); s != "" {
					segs = append(segs, s)
				}
				cur.Reset()
				prevTerm = false
				continue
			}
			cur.WriteRune(ch)
			prevTerm = ch == '.' || ch == '!' || ch == '?'
		}
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// crusherTokens lowercases runs of alphanumerics and underscores. These are the
// shingle keys, not the output: kept segments stay verbatim.
func crusherTokens(text string) []string {
	var out []string
	var cur strings.Builder
	for _, ch := range text {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			cur.WriteRune(unicode.ToLower(ch))
		} else if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// shingles returns the set of k-word windows, joined by a separator that cannot
// occur in a token.
func shingles(words []string, k int) map[string]bool {
	set := map[string]bool{}
	if len(words) == 0 {
		return set
	}
	if len(words) < k {
		// Short segment: emit every sub-window so two identical or overlapping
		// short segments still near-dup-match each other. They cannot match a
		// longer segment's k-grams, but short segments are score-penalised and
		// rarely survive selection anyway.
		for size := 1; size <= len(words); size++ {
			for i := 0; i+size <= len(words); i++ {
				set[strings.Join(words[i:i+size], "\x01")] = true
			}
		}
		return set
	}
	for i := 0; i+k <= len(words); i++ {
		set[strings.Join(words[i:i+k], "\x01")] = true
	}
	return set
}

// isSalient marks a word as structurally interesting: it carries a digit, is a
// failure keyword, is an all-caps acronym, or looks like a dotted identifier.
func isSalient(word string) bool {
	for _, ch := range word {
		if ch >= '0' && ch <= '9' {
			return true
		}
	}
	lower := strings.ToLower(strings.TrimFunc(word, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}))
	if textCrusherKeywords[lower] {
		return true
	}
	alpha := 0
	allUpper := true
	for _, ch := range word {
		if unicode.IsLetter(ch) {
			alpha++
			if !unicode.IsUpper(ch) {
				allUpper = false
			}
		}
	}
	if alpha >= 2 && allUpper {
		return true
	}
	if dot := strings.Index(word, "."); dot > 0 && dot+1 < len(word) {
		a, b := word[:dot], word[dot+1:]
		if identStart(a) && identStart(b) {
			return true
		}
	}
	return false
}

func identStart(s string) bool {
	for _, ch := range s {
		return unicode.IsLetter(ch) || ch == '_'
	}
	return false
}

// CountSegments reports how many segments text would split into. Callers use it
// to decide whether crushing is worth attempting at all.
func CountSegments(text string) int { return len(splitSegments(text)) }
