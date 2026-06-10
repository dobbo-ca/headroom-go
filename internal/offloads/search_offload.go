package offloads

import (
	"fmt"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// SearchOffload wraps a SearchCompressor as an OffloadTransform for grep/ripgrep
// output. It is IMPLEMENTED but intentionally NOT registered by the default
// pipeline builder (matching upstream). EstimateBloat scores how tightly matches
// cluster into few files; Apply delegates to the compressor.
type SearchOffload struct {
	compressor *compress.SearchCompressor
	bias       float64
}

const (
	searchMinMatches       = 10
	searchClusterThreshold = 10.0
	searchConfidence       = 0.85
)

// NewSearchOffload builds a SearchOffload around the given compressor.
func NewSearchOffload(c *compress.SearchCompressor) *SearchOffload {
	return &SearchOffload{compressor: c, bias: 0.0}
}

func (*SearchOffload) Name() string { return "search_offload" }

func (*SearchOffload) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.SearchResults}
}

func (*SearchOffload) Confidence() float32 { return searchConfidence }

// EstimateBloat counts file-prefixed match lines and unique file prefixes, then
// scores the average matches-per-file above 1.0. Returns 0 on empty input, too
// few matches, no files, or no clustering.
func (o *SearchOffload) EstimateBloat(content string) float32 {
	if content == "" {
		return 0
	}
	total := 0
	files := make(map[string]struct{})
	for _, line := range splitLinesRust(content) {
		if file, ok := extractFilePrefix(line); ok {
			total++
			files[file] = struct{}{}
		}
	}
	if total < searchMinMatches || len(files) == 0 {
		return 0
	}
	avg := float32(total) / float32(len(files))
	if avg <= 1.0 {
		return 0
	}
	score := (avg - 1.0) / float32(searchClusterThreshold)
	return clamp01(score)
}

// Apply delegates to the wrapped SearchCompressor. A missing CacheKey becomes
// ErrSkipped; the compressor owns its markers, CacheKey, and store.Put.
func (o *SearchOffload) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	r := o.compressor.Compress(content, ctx.Query, o.bias, store)
	if r.CacheKey == "" {
		return transform.OffloadOutput{}, fmt.Errorf("search_offload: no cache_key emitted: %w", transform.ErrSkipped)
	}
	return fromLengths(len(content), r.Compressed, r.CacheKey), nil
}

// extractFilePrefix byte-scans a search-result line for a file-path prefix
// terminated by the first ':' or '-' separator that is immediately preceded by at
// least one ASCII digit. A leading Windows drive ("C:") is skipped so its colon is
// not mistaken for the line-number separator. Returns line[:i] and true, or "".
func extractFilePrefix(line string) (string, bool) {
	b := []byte(line)
	n := len(b)
	if n == 0 {
		return "", false
	}
	start := 0
	if n >= 2 && b[1] == ':' && asciiAlpha(b[0]) {
		start = 2
	}
	for i := start; i < n; i++ {
		if b[i] != ':' && b[i] != '-' {
			continue
		}
		// Require >=1 ASCII digit immediately before the separator.
		if i > 0 && asciiDigit(b[i-1]) {
			return line[:i], true
		}
	}
	return "", false
}

func asciiAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func asciiDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
