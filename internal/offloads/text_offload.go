package offloads

import (
	"fmt"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/detect"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

const (
	textConfidence = 0.7
	// textMinSegments is the floor below which crushing cannot pay for its own
	// CCR marker. It mirrors the compressor's own MinSegmentsForCrush.
	textMinSegments = 6
	// textMinChars keeps the transform off short prose, where the marker plus
	// the kept sentences reliably costs more than the original.
	textMinChars = 1024
)

// TextOffload is the plain-text arm of the router. Before it existed the
// PlainText bucket had NO compressor at all — 59.9 MB, 34.9% of a real
// 171.9 MB corpus, reached by nothing.
//
// It stores the original under a CCR key and emits the kept sentences plus a
// retrieval marker, so nothing is lost: the model can call headroom_retrieve
// for the full text.
type TextOffload struct {
	crusher *compress.TextCrusher
}

// NewTextOffload builds a TextOffload around the given crusher.
func NewTextOffload(c *compress.TextCrusher) *TextOffload {
	return &TextOffload{crusher: c}
}

func (*TextOffload) Name() string { return "text_offload" }

func (*TextOffload) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.PlainText}
}

func (*TextOffload) Confidence() float32 { return textConfidence }

// EstimateBloat reports how much of the content looks droppable: the fraction
// of characters the crusher's target ratio would leave behind. Short or
// few-segment content scores 0 so the router does not pick it.
func (o *TextOffload) EstimateBloat(content string) float32 {
	if len(content) < textMinChars {
		return 0
	}
	if n := compress.CountSegments(content); n < textMinSegments {
		return 0
	}
	return 0.5
}

// Apply crushes the prose and stashes the original. A result that kept
// everything is skipped rather than returned, so the I5 gate is never asked to
// judge a marker with no saving behind it.
func (o *TextOffload) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	// PROTECTED READS. Raw file content must stay byte-exact: the agent needs
	// the exact bytes to produce a precise patch. Dropping sentences from it
	// was observed upstream (SWE-bench, mini-swe-agent) to make the agent
	// re-read the same file to recover detail — turn inflation — and, when
	// recovery failed, to resolve the task wrongly.
	//
	// This arm carries most of the risk in this transform, because the code
	// detector recognises only a handful of languages: Ruby, C, SQL, shell and
	// Markdown all land in PlainText. Protecting reads is what keeps them safe.
	if detect.ReadOutputIsProtected(ctx.ProducingTool, ctx.ToolCommand, transform.PlainText) {
		return transform.OffloadOutput{}, fmt.Errorf(
			"text_offload: %s output is a protected file read: %w", ctx.ProducingTool, transform.ErrSkipped)
	}

	r := o.crusher.Compress(content, ctx.Query, 0)
	// Efficiency early-out, NOT a correctness guard: the I5 token gate would
	// reject a marker with no saving behind it anyway. This just avoids a
	// store write and a marker build on every uncompressible text block, of
	// which a real corpus has tens of thousands. No test covers it, because
	// removing it changes no observable outcome.
	if r.Compressed == content || r.KeptSegments == r.TotalSegments {
		return transform.OffloadOutput{}, fmt.Errorf("text_offload: nothing dropped: %w", transform.ErrSkipped)
	}
	key := ccr.ComputeKeyMD5([]byte(content))
	store.Put(key, content)
	out := r.Compressed + fmt.Sprintf(
		"\n[%d of %d sentences kept. Retrieve the full text: hash=%s]",
		r.KeptSegments, r.TotalSegments, key)
	return fromLengths(len(content), out, key), nil
}
