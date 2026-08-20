package compress

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DiffConfig holds the compressor's own knob. See LogConfig for why this is
// not a field on pipeline.Config.
type DiffConfig struct {
	MaxHunks int
}

// DefaultDiffConfig returns the documented default.
func DefaultDiffConfig() DiffConfig { return DiffConfig{MaxHunks: 40} }

// DiffCompressor drops noise hunks from a unified diff and caps how many
// survive. The original goes to the CCR store, so every dropped hunk stays
// recoverable through the emitted key.
type DiffCompressor struct{ cfg DiffConfig }

// NewDiffCompressor builds a DiffCompressor. A non-positive MaxHunks falls
// back to the default, so a partly-filled config cannot drop every hunk.
func NewDiffCompressor(cfg DiffConfig) *DiffCompressor {
	if cfg.MaxHunks <= 0 {
		cfg.MaxHunks = DefaultDiffConfig().MaxHunks
	}
	return &DiffCompressor{cfg: cfg}
}

func (c *DiffCompressor) Name() string { return "diff_compressor" }

func (c *DiffCompressor) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.GitDiff}
}

func (c *DiffCompressor) Confidence() float32 { return 0.75 }

// EstimateBloat is a cheap structural sniff, per the interface contract: it
// counts @@ markers and must NOT parse hunk bodies.
func (c *DiffCompressor) EstimateBloat(content string) float32 {
	hunks := strings.Count(content, "\n@@")
	if strings.HasPrefix(content, "@@") {
		hunks++
	}
	if hunks == 0 {
		return 0
	}
	score := float32(hunks) / float32(c.cfg.MaxHunks)
	if score > 1 {
		score = 1
	}
	return score
}

// Apply drops noise hunks, caps the remainder, and appends the CCR marker.
//
// The key is computed before anything is stored, and the store is written only
// when the result is actually shorter — a no-op offload must not leave an
// entry behind.
func (c *DiffCompressor) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	if content == "" {
		return transform.OffloadOutput{}, fmt.Errorf("diff_compressor: %w", transform.ErrInvalidInput)
	}

	preamble, hunks := parseDiff(content)
	if len(hunks) == 0 {
		return transform.OffloadOutput{}, fmt.Errorf("diff_compressor: no hunks: %w", transform.ErrSkipped)
	}

	key := ccr.ComputeKey([]byte(content))

	out := make([]string, 0, len(preamble)+len(hunks)*4)
	out = append(out, preamble...)

	kept, dropped := 0, 0
	for _, h := range hunks {
		if isNoise(h) || kept >= c.cfg.MaxHunks {
			dropped++
			continue
		}
		kept++
		out = append(out, h.header...)
		out = append(out, h.body...)
	}

	// The output is lossy (something was left out), so it must carry an
	// in-band marker pointing at the recoverable original regardless of
	// what was dropped — gating this on hunk count alone missed lossy
	// output caused by other omissions.
	if dropped > 0 {
		out = append(out, fmt.Sprintf("... %d hunk(s) dropped as noise or over the cap", dropped))
	}
	out = append(out, ccr.MarkerFor(key))
	joined := strings.Join(out, "\n")

	// The never-inflate check must run after the marker/drop-line are
	// appended: those bytes count against the input too, and a small
	// dropped hunk can be outweighed by them.
	if len(joined) >= len(content) {
		return transform.OffloadOutput{Output: content, BytesSaved: 0}, nil
	}

	store.Put(key, content)
	return transform.OffloadOutput{
		Output:     joined,
		BytesSaved: len(content) - len(joined),
		CacheKey:   key,
	}, nil
}
