package livezone

import (
	"bytes"
	"encoding/json"
	"sort"
)

// replacement is one byte range of the original body and the bytes that take
// its place.
type replacement struct {
	start int
	end   int
	repl  []byte
}

// applyReplacements splices reps into orig and reports the ranges it
// rewrote. Every byte outside a replacement is copied verbatim — this
// function is where invariant I1 is enforced.
//
// reps are sorted by ascending start. A replacement overlapping an earlier
// one is dropped rather than applied at a shifted offset, so a planning bug
// degrades to less compression, never to a corrupt body.
func applyReplacements(orig []byte, reps []replacement) ([]byte, []Range) {
	if len(reps) == 0 {
		return orig, nil
	}

	sorted := make([]replacement, len(reps))
	copy(sorted, reps)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })

	removed, added := 0, 0
	for _, r := range sorted {
		removed += r.end - r.start
		added += len(r.repl)
	}
	out := make([]byte, 0, len(orig)-removed+added)
	ranges := make([]Range, 0, len(sorted))

	cursor := 0
	for _, r := range sorted {
		if r.start < cursor || r.end < r.start || r.end > len(orig) {
			// Overlapping or out-of-bounds: drop it.
			continue
		}
		out = append(out, orig[cursor:r.start]...)
		out = append(out, r.repl...)
		ranges = append(ranges, Range{Start: r.start, End: r.end, NewLen: len(r.repl)})
		cursor = r.end
	}
	out = append(out, orig[cursor:]...)

	if len(ranges) == 0 {
		return orig, nil
	}
	return out, ranges
}

// encodeJSONString returns the JSON string literal for s, quotes included.
//
// HTML escaping is off: json.Marshal would rewrite <, > and & as <,
// > and &, inflating a payload we are trying to shrink. Encoder
// appends a newline, which is trimmed.
func encodeJSONString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// json cannot fail to encode a Go string; fall back to the
		// escaping form rather than returning invalid JSON.
		b, _ := json.Marshal(s)
		return b
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
