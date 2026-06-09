package pipeline

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Result is the outcome of a pipeline run.
type Result struct {
	Output       string
	BytesSaved   int
	StepsApplied []string
	CacheKeys    []string // from accepted OFFLOADS only; reformats never add keys
}

// Pipeline orchestrates reformats (lossless) then offloads (info-preserving),
// dispatched by ContentType. Sequential by design for v0.
type Pipeline struct {
	reformatsByType map[transform.ContentType][]transform.ReformatTransform
	offloadsByType  map[transform.ContentType][]transform.OffloadTransform
	config          Config
}

// Builder assembles a Pipeline, registering each transform under every type in
// its AppliesTo() list.
type Builder struct {
	reformats []transform.ReformatTransform
	offloads  []transform.OffloadTransform
	config    Config
	hasConfig bool
}

func NewBuilder() *Builder { return &Builder{} }

func (b *Builder) WithReformat(t transform.ReformatTransform) *Builder {
	b.reformats = append(b.reformats, t)
	return b
}
func (b *Builder) WithOffload(t transform.OffloadTransform) *Builder {
	b.offloads = append(b.offloads, t)
	return b
}
func (b *Builder) WithConfig(c Config) *Builder {
	b.config, b.hasConfig = c, true
	return b
}

func (b *Builder) Build() *Pipeline {
	p := &Pipeline{
		reformatsByType: map[transform.ContentType][]transform.ReformatTransform{},
		offloadsByType:  map[transform.ContentType][]transform.OffloadTransform{},
		config:          b.config,
	}
	if !b.hasConfig {
		p.config = DefaultConfig()
	}
	for _, t := range b.reformats {
		for _, ct := range t.AppliesTo() {
			p.reformatsByType[ct] = append(p.reformatsByType[ct], t)
		}
	}
	for _, t := range b.offloads {
		for _, ct := range t.AppliesTo() {
			p.offloadsByType[ct] = append(p.offloadsByType[ct], t)
		}
	}
	return p
}

// Run compresses content of the given type. It always returns some output (the
// input verbatim if every stage skips). Errors from transforms are swallowed
// (skip-and-continue); they never propagate or panic.
func (p *Pipeline) Run(content string, ct transform.ContentType, ctx transform.CompressionContext, store ccr.Store) Result {
	originalLen := len(content)
	current := content
	var steps []string
	bytesSaved := 0

	// Phase 1: reformats, sequential, registration order, early-stop.
	for _, rf := range p.reformatsByType[ct] {
		if originalLen > 0 && float64(len(current))/float64(originalLen) <= p.config.ReformatTargetRatio {
			break // already small enough
		}
		out, err := rf.Apply(current)
		if err != nil || out.BytesSaved == 0 {
			continue
		}
		current = out.Output
		steps = append(steps, rf.Name())
		bytesSaved = saturatingAdd(bytesSaved, out.BytesSaved)
	}

	// Phase 2: offloads, gated.
	reformatRatio := 1.0
	if originalLen > 0 {
		reformatRatio = float64(len(current)) / float64(originalLen)
	}
	var cacheKeys []string
	for _, off := range p.offloadsByType[ct] {
		score := off.EstimateBloat(current)
		run := float64(score) >= p.config.BloatThreshold ||
			(reformatRatio > p.config.OffloadFallbackRatio && score > 0)
		if !run {
			continue
		}
		out, err := off.Apply(current, ctx, store)
		if err != nil || out.BytesSaved == 0 {
			continue
		}
		current = out.Output
		steps = append(steps, off.Name())
		bytesSaved = saturatingAdd(bytesSaved, out.BytesSaved)
		cacheKeys = append(cacheKeys, out.CacheKey)
	}

	return Result{Output: current, BytesSaved: bytesSaved, StepsApplied: steps, CacheKeys: cacheKeys}
}

func saturatingAdd(a, b int) int {
	s := a + b
	if s < a {
		return int(^uint(0) >> 1) // max int
	}
	return s
}
