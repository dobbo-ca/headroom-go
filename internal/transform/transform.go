// Package transform defines the content types and the Reformat/Offload
// transform interfaces that the compression pipeline composes.
package transform

import (
	"errors"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// ContentType is the routing key produced by the detector.
type ContentType int

const (
	JsonArray ContentType = iota
	SourceCode
	SearchResults
	BuildOutput
	GitDiff
	Html
	PlainText
)

// String returns the stable tag used in config and logs. Unknown values fall
// back to "text" so routing never panics.
func (c ContentType) String() string {
	switch c {
	case JsonArray:
		return "json_array"
	case SourceCode:
		return "source_code"
	case SearchResults:
		return "search"
	case BuildOutput:
		return "build"
	case GitDiff:
		return "diff"
	case Html:
		return "html"
	default:
		return "text"
	}
}

// CompressionContext carries per-call relevance and budget hints.
type CompressionContext struct {
	Query       string
	TokenBudget *int
}

// Sentinel errors. ALL mean "skip this transform, continue the pipeline,
// never panic". Transforms wrap these with %w.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrSkipped      = errors.New("skipped")
	ErrInternal     = errors.New("internal")
)

// ReformatOutput is the result of a lossless transform. Output is semantically
// equivalent to the input; there is no CCR backing.
type ReformatOutput struct {
	Output     string
	BytesSaved int
}

// OffloadOutput is the result of an information-preserving transform. Output is
// a subset of the input; the original is stashed in a CCR store under CacheKey.
type OffloadOutput struct {
	Output     string
	BytesSaved int
	CacheKey   string
}

// ReformatTransform packs content denser without dropping information. Runs
// first in the pipeline (surviving bytes must round-trip semantically).
type ReformatTransform interface {
	Name() string
	AppliesTo() []ContentType
	Apply(content string) (ReformatOutput, error)
}

// OffloadTransform subsets content and stashes the original in the store.
// Apply runs only after EstimateBloat clears the pipeline's threshold and MUST
// emit a CacheKey that resolves in the provided store.
type OffloadTransform interface {
	Name() string
	AppliesTo() []ContentType
	EstimateBloat(content string) float32 // 0..1, cheap structural sniff, NO full pass
	Apply(content string, ctx CompressionContext, store ccr.Store) (OffloadOutput, error)
	Confidence() float32
}
