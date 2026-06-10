// Package offloads holds the v0.1 OffloadTransforms: thin wrappers that delegate
// to the compress engines (log/diff/search) plus two self-contained offloads
// (diff_noise and json_offload). Each subsets content and stashes the original
// in a CCR store, matching upstream headroom's offload behavior.
package offloads

import (
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// fromLengths builds an OffloadOutput with saturating bytes_saved: BytesSaved is
// max(0, inputLen-len(output)) so a transform never reports negative savings even
// when the output (rarely) ends up longer than the input.
func fromLengths(inputLen int, output, cacheKey string) transform.OffloadOutput {
	saved := inputLen - len(output)
	if saved < 0 {
		saved = 0
	}
	return transform.OffloadOutput{Output: output, BytesSaved: saved, CacheKey: cacheKey}
}

// clamp01 clamps x to [0,1].
func clamp01(x float32) float32 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// splitLinesRust splits s into lines with Rust str::lines() semantics: split on
// '\n', strip one trailing '\r' per line, and drop the single trailing empty
// element when s ends with '\n'. An empty string yields no lines. The offloads
// mirror upstream content.lines() for line counts and byte accounting.
func splitLinesRust(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

// Crusher is the SmartCrusher seam (Plan 3). The Plan-2 default is passthrough,
// so JsonOffload.Apply always skips cleanly until the real crusher lands.
type Crusher interface {
	Crush(content, query string, bias float64) CrushResult
}

// CrushResult is the output of a Crusher pass.
type CrushResult struct {
	Compressed  string
	WasModified bool
}

// passthroughCrusher is the Plan-2 default Crusher: it never modifies content.
type passthroughCrusher struct{}

func (passthroughCrusher) Crush(content, _ string, _ float64) CrushResult {
	return CrushResult{Compressed: content, WasModified: false}
}
