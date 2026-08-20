package compress

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// LogConfig holds the compressor's own knobs.
//
// These do NOT live in pipeline.Config: that struct holds the three
// orchestrator gating thresholds, and widening it per compressor would make
// the orchestrator know about every compressor that exists.
type LogConfig struct {
	HeadLines         int
	TailLines         int
	MinLinesToOffload int
}

// DefaultLogConfig returns the documented defaults.
func DefaultLogConfig() LogConfig {
	return LogConfig{HeadLines: 50, TailLines: 50, MinLinesToOffload: 200}
}

// LogCompressor squeezes build and test output through five stages, then
// stashes the original in the CCR store so every drop stays recoverable.
type LogCompressor struct{ cfg LogConfig }

// NewLogCompressor builds a LogCompressor. Any non-positive field falls back
// to its default, so a partly-filled config cannot produce a zero head or tail.
func NewLogCompressor(cfg LogConfig) *LogCompressor {
	d := DefaultLogConfig()
	if cfg.HeadLines <= 0 {
		cfg.HeadLines = d.HeadLines
	}
	if cfg.TailLines <= 0 {
		cfg.TailLines = d.TailLines
	}
	if cfg.MinLinesToOffload <= 0 {
		cfg.MinLinesToOffload = d.MinLinesToOffload
	}
	return &LogCompressor{cfg: cfg}
}

func (c *LogCompressor) Name() string { return "log_compressor" }

func (c *LogCompressor) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.BuildOutput}
}

// Confidence reports how much the pipeline should trust this transform.
func (c *LogCompressor) Confidence() float32 { return 0.8 }

// EstimateBloat is a cheap structural sniff, per the interface contract: it
// counts lines and must NOT run the stages.
func (c *LogCompressor) EstimateBloat(content string) float32 {
	lines := strings.Count(content, "\n") + 1
	if lines <= 1 {
		return 0
	}
	score := float32(lines) / float32(c.cfg.MinLinesToOffload)
	if score > 1 {
		score = 1
	}
	return score
}

// offloadMiddle keeps the first head and last tail lines and replaces the
// middle with marker. It is a no-op below min lines, or when head plus tail
// already covers the input.
func offloadMiddle(s string, head, tail, min int, marker string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < min || head+tail >= len(lines) {
		return s
	}
	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, marker)
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// Apply runs the five stages in order.
//
// The key is computed before anything is stored, and the store is written only
// when the result is actually shorter — a no-op offload must not leave an
// entry behind.
func (c *LogCompressor) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	if content == "" {
		return transform.OffloadOutput{}, fmt.Errorf("log_compressor: %w", transform.ErrInvalidInput)
	}

	key := ccr.ComputeKey([]byte(content))

	out := stripANSI(content)
	out = collapseRuns(out)
	out = dedupWarnings(out)
	out = dropProgress(out)
	out = offloadMiddle(out, c.cfg.HeadLines, c.cfg.TailLines, c.cfg.MinLinesToOffload, ccr.MarkerFor(key))

	if len(out) >= len(content) {
		return transform.OffloadOutput{Output: content, BytesSaved: 0}, nil
	}

	store.Put(key, content)
	return transform.OffloadOutput{
		Output:     out,
		BytesSaved: len(content) - len(out),
		CacheKey:   key,
	}, nil
}
