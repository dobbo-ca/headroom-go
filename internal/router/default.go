package router

import (
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
)

// NewDefault wires the v0.1 heuristic compressors into a Router: JSON minify +
// log templating reformats, and the diff_noise/diff/json/log offloads. SearchOffload
// is intentionally NOT registered (matches upstream). JsonOffload uses the Plan-2
// passthrough crusher seam (real SmartCrusher arrives in Plan 3).
//
// Registration order (= run order) matches upstream offloads/mod.rs:
//   - reformats: JsonMinifier ([JsonArray]) -> LogTemplate ([BuildOutput])
//   - offloads:  DiffNoise ([GitDiff]) -> DiffOffload ([GitDiff]) ->
//     JsonOffload ([JsonArray]) -> LogOffload ([BuildOutput])
func NewDefault() *Router {
	p := pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithReformat(reformats.LogTemplate{}).
		WithOffload(offloads.NewDiffNoise()).
		WithOffload(offloads.NewDiffOffload(compress.NewDiffCompressor())).
		WithOffload(offloads.NewJsonOffload()).
		WithOffload(offloads.NewLogOffload(compress.NewLogCompressor())).
		Build()
	return New(p)
}
