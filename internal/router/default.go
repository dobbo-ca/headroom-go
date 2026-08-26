package router

import (
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher"
)

// NewDefault wires the v0.1 heuristic compressors into a Router: JSON minify +
// log templating reformats, and the read_outline/diff_noise/diff/json/log/text offloads.
// SearchOffload is intentionally NOT registered (matches upstream). JsonOffload uses
// the Plan-3 SmartCrusher, injected here at the composition root — smartcrusher imports
// offloads for the seam type, so offloads cannot import smartcrusher; this router
// is the only production package free to import both.
//
// Registration order (= run order):
//   - reformats: JsonMinifier ([JsonArray]) -> LogTemplate ([BuildOutput])
//   - offloads:  ReadOutline (FIRST, [PlainText, BuildOutput, SearchResults, SourceCode]) ->
//     DiffNoise ([GitDiff]) -> DiffOffload ([GitDiff]) ->
//     JsonOffload ([JsonArray]) -> LogOffload ([BuildOutput]) ->
//     TextOffload ([PlainText])
//
// ReadOutline runs FIRST so it precedes LogOffload and TextOffload in every routing
// list. It declines explicitly on non-Go extensions and non-Read tools, so it only
// touches Go source Read output. LogOffload and TextOffload both check the
// read-protection gate, which now recognizes code file extensions and protects them.
func NewDefault() *Router {
	p := pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithReformat(reformats.LogTemplate{}).
		WithOffload(offloads.NewReadOutline()).
		WithOffload(offloads.NewDiffNoise()).
		WithOffload(offloads.NewDiffOffload(compress.NewDiffCompressor())).
		WithOffload(offloads.NewJsonOffloadWith(smartcrusher.NewSmartCrusher(smartcrusher.DefaultConfig()))).
		WithOffload(offloads.NewLogOffload(compress.NewLogCompressor())).
		WithOffload(offloads.NewTextOffload(compress.NewTextCrusher(compress.DefaultTextCrusherConfig()))).
		Build()
	return New(p)
}
