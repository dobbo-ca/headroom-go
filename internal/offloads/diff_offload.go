package offloads

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DiffOffload wraps a DiffCompressor as an OffloadTransform for git diffs.
// EstimateBloat measures the in-hunk context-to-change ratio; Apply delegates to
// the compressor and propagates its CacheKey (or Skips when it emits none).
type DiffOffload struct {
	compressor *compress.DiffCompressor
}

const (
	diffBloatMinLines      = 50
	diffNormalContextRatio = 0.6
	diffConfidence         = 0.85
)

// NewDiffOffload builds a DiffOffload around the given compressor.
func NewDiffOffload(c *compress.DiffCompressor) *DiffOffload {
	return &DiffOffload{compressor: c}
}

func (*DiffOffload) Name() string { return "diff_offload" }

func (*DiffOffload) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.GitDiff}
}

func (*DiffOffload) Confidence() float32 { return diffConfidence }

// EstimateBloat counts in-hunk context vs change lines in a single pass and maps
// the context ratio above normalContextRatio linearly into [0,1]. Returns 0 on
// empty input, fewer than diffBloatMinLines lines, no in-hunk lines, or a context
// ratio at or below normal.
func (o *DiffOffload) EstimateBloat(content string) float32 {
	if content == "" {
		return 0
	}
	total := 0
	context := 0
	change := 0
	inHunk := false
	for _, line := range splitLinesRust(content) {
		total++
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			continue
		case strings.HasPrefix(line, "diff --git"):
			inHunk = false
			continue
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+', '-':
			change++
		case ' ':
			context++
		}
	}
	if total < diffBloatMinLines {
		return 0
	}
	denom := context + change
	if denom == 0 {
		return 0
	}
	ratio := float32(context) / float32(denom)
	const normal = float32(diffNormalContextRatio)
	if ratio <= normal {
		return 0
	}
	span := 1.0 - normal
	if span <= 0 {
		return 1
	}
	return clamp01((ratio - normal) / span)
}

// Apply delegates to the wrapped DiffCompressor, passing ctx.Query as the diff
// context. A missing CacheKey becomes ErrSkipped.
func (o *DiffOffload) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	r := o.compressor.Compress(content, ctx.Query, store)
	if r.CacheKey == "" {
		return transform.OffloadOutput{}, fmt.Errorf("diff_offload: diff compressor did not emit a cache_key: %w", transform.ErrSkipped)
	}
	return fromLengths(len(content), r.Compressed, r.CacheKey), nil
}
