package router

import (
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher"
)

// NewDefault wires the v0.1 heuristic compressors into a Router: JSON minify +
// log templating reformats, and the diff_noise/diff/json/log/text offloads.
// SearchOffload is intentionally NOT registered (matches upstream). JsonOffload uses
// the Plan-3 SmartCrusher, injected here at the composition root — smartcrusher imports
// offloads for the seam type, so offloads cannot import smartcrusher; this router
// is the only production package free to import both.
//
// ReadOutline is DISABLED by default: measured net token loss of 223,365 (−0.65pp of
// all tool_result content, −2.20% of baseline saving). The outline wins 961,327 tokens
// on Go reads but costs 1,185,529 tokens leaked through the same root cause (detector
// cannot see through Read's line-number prefixes) on .md/.cs/.svelte/.sh/.yml. To enable,
// uncomment the ReadOutline line below. To make it worth enabling: fix the detector's
// blindness to line-number prefixes once, for all extensions, and re-measure.
//
// Registration order (= run order):
//   - reformats: JsonMinifier ([JsonArray]) -> LogTemplate ([BuildOutput])
//   - offloads:  DiffNoise ([GitDiff]) -> DiffOffload ([GitDiff]) ->
//     JsonOffload ([JsonArray]) -> LogOffload ([BuildOutput]) ->
//     TextOffload ([PlainText])
func NewDefault() *Router {
	p := pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithReformat(reformats.LogTemplate{}).
		// WithOffload(offloads.NewReadOutline()).  // DISABLED: net loss, see comment above
		WithOffload(offloads.NewDiffNoise()).
		WithOffload(offloads.NewDiffOffload(compress.NewDiffCompressor())).
		WithOffload(offloads.NewJsonOffloadWith(smartcrusher.NewSmartCrusher(smartcrusher.DefaultConfig()))).
		WithOffload(offloads.NewLogOffload(compress.NewLogCompressor())).
		WithOffload(offloads.NewTextOffload(compress.NewTextCrusher(compress.DefaultTextCrusherConfig()))).
		Build()
	return New(p)
}
