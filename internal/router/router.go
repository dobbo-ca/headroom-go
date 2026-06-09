// Package router detects a content block's type and runs it through the
// compression pipeline. It is the single seam the entrypoints (MCP/proxy/CLI)
// call to compress one piece of content.
package router

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/detect"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Router pairs the content detector with a compression pipeline.
type Router struct {
	pipeline *pipeline.Pipeline
}

// New builds a Router over the given pipeline.
func New(p *pipeline.Pipeline) *Router { return &Router{pipeline: p} }

// Detect classifies content.
func (r *Router) Detect(content string) detect.DetectionResult {
	return detect.DetectContentType(content)
}

// Compress detects the content type and runs the pipeline. With no registered
// transforms it returns the input verbatim (faithful passthrough).
func (r *Router) Compress(content string, ctx transform.CompressionContext, store ccr.Store) pipeline.Result {
	d := detect.DetectContentType(content)
	return r.pipeline.Run(content, d.Type, ctx, store)
}
