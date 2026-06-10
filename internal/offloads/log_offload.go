package offloads

import (
	"fmt"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/signals"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// LogOffload wraps a LogCompressor as an OffloadTransform for build/log output.
// EstimateBloat is a cheap structural sniff (repetition + low-priority dilution);
// Apply delegates entirely to the compressor and propagates its CacheKey (or
// Skips when the compressor emits none — it never fabricates a key).
type LogOffload struct {
	compressor *compress.LogCompressor
	detector   signals.LineImportanceDetector
	bias       float64
}

const (
	logBloatMinLines      = 50
	logBloatSampleSize    = 100
	logHighPriorityThresh = 0.4 // a sampled line is low-priority if Priority <= this
	logUniquenessWeight   = 0.5
	logDilutionWeight     = 0.5
	logConfidence         = 0.85
)

// NewLogOffload builds a LogOffload around the given compressor with a default
// KeywordDetector and zero bias (matching upstream LogOffload::with_compressor
// defaults).
func NewLogOffload(c *compress.LogCompressor) *LogOffload {
	return &LogOffload{compressor: c, detector: signals.NewKeywordDetector(), bias: 0.0}
}

func (*LogOffload) Name() string { return "log_offload" }

func (*LogOffload) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.BuildOutput}
}

func (*LogOffload) Confidence() float32 { return logConfidence }

// EstimateBloat samples the first 100 lines for repetition (unique ratio) and
// dilution (low-priority ratio), then combines them. Returns 0 on empty input or
// when the full line count is below logBloatMinLines.
func (o *LogOffload) EstimateBloat(content string) float32 {
	if content == "" {
		return 0
	}
	unique := make(map[string]struct{})
	sampled := 0
	lowPriority := 0
	for _, line := range splitLinesRust(content) {
		if sampled >= logBloatSampleSize {
			break
		}
		sampled++
		unique[line] = struct{}{}
		if o.detector.Score(line, signals.Log).Priority <= logHighPriorityThresh {
			lowPriority++
		}
	}
	total := len(splitLinesRust(content))
	if total < logBloatMinLines || sampled == 0 {
		return 0
	}
	repetition := 1.0 - float32(len(unique))/float32(sampled)
	dilution := float32(lowPriority) / float32(sampled)
	score := repetition*logUniquenessWeight + dilution*logDilutionWeight
	return clamp01(score)
}

// Apply delegates to the wrapped LogCompressor. A missing CacheKey becomes
// ErrSkipped; the compressor itself stashed the original under its CacheKey.
func (o *LogOffload) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	r := o.compressor.Compress(content, store)
	if r.CacheKey == "" {
		return transform.OffloadOutput{}, fmt.Errorf("log_offload: no cache_key emitted: %w", transform.ErrSkipped)
	}
	return fromLengths(len(content), r.Compressed, r.CacheKey), nil
}
