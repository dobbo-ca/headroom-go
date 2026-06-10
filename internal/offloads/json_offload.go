package offloads

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// JsonOffload wraps a Crusher (the SmartCrusher seam) as an OffloadTransform for
// JSON arrays of objects. EstimateBloat counts row separators as a cheap shape
// sniff. With the Plan-2 passthrough crusher, Apply always skips cleanly; Plan 3
// swaps in the real SmartCrusher via the Crusher seam.
type JsonOffload struct {
	crusher        Crusher
	minArrayRows   int
	saturationRows int
}

const jsonConfidence = 0.85

// NewJsonOffload builds a JsonOffload with the Plan-2 passthrough crusher and the
// upstream config defaults (minArrayRows=5, saturationRows=50).
func NewJsonOffload() *JsonOffload {
	return &JsonOffload{
		crusher:        passthroughCrusher{},
		minArrayRows:   5,
		saturationRows: 50,
	}
}

func (*JsonOffload) Name() string { return "json_offload" }

func (*JsonOffload) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.JsonArray}
}

func (*JsonOffload) Confidence() float32 { return jsonConfidence }

// EstimateBloat counts row separators ("},{", "}, {", "},\n") as a cheap proxy for
// the number of array rows. Returns 0 on empty input, non-array shape, or too few
// separators.
func (o *JsonOffload) EstimateBloat(content string) float32 {
	if content == "" {
		return 0
	}
	if !strings.HasPrefix(strings.TrimLeft(content, " \t\r\n"), "[") {
		return 0
	}
	seps := countRowSeparators(content)
	if seps < o.minArrayRows-1 {
		return 0
	}
	sat := o.saturationRows - 1
	if sat < 1 {
		sat = 1
	}
	return clamp01(float32(seps) / float32(sat))
}

// Apply delegates to the Crusher. With the passthrough crusher this always skips
// (WasModified=false). On a real crush it stashes the original under a CCR key and
// appends the wrapper marker.
func (o *JsonOffload) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	r := o.crusher.Crush(content, ctx.Query, 0.0)
	if !r.WasModified {
		return transform.OffloadOutput{}, fmt.Errorf("json_offload: smart crusher returned passthrough: %w", transform.ErrSkipped)
	}
	if len(r.Compressed) >= len(content) {
		return transform.OffloadOutput{}, fmt.Errorf("json_offload: no savings after crush: %w", transform.ErrSkipped)
	}
	key := ccr.ComputeKeyMD5([]byte(content))
	store.Put(key, content)
	out := r.Compressed + "\n[json_offload CCR: hash=" + key + "]"
	return fromLengths(len(content), out, key), nil
}

// countRowSeparators counts the three fixed row-separator substrings. It does not
// parse JSON and tolerates false positives inside quoted strings (the estimate is
// order-of-magnitude only).
func countRowSeparators(content string) int {
	return strings.Count(content, "},{") +
		strings.Count(content, "}, {") +
		strings.Count(content, "},\n")
}
