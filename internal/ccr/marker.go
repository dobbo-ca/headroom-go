package ccr

import (
	"fmt"
	"regexp"
)

// Three marker surfaces exist and are intentionally NOT unified:
//   canonical: <<ccr:HASH>>                       (live-zone block offload)
//   cell:      <<ccr:HASH,KIND,SIZE>>             (compaction opaque cell)
//   lossy:     <<ccr:HASH N_rows_offloaded>>      (lossy row drop)

const markerPrefix = "<<ccr:"
const markerSuffix = ">>"

var canonicalRe = regexp.MustCompile(`^<<ccr:([0-9a-f]{24})>>$`)

// MarkerFor builds the canonical live-zone marker.
func MarkerFor(hash string) string { return markerPrefix + hash + markerSuffix }

// MarkerForCell builds a compaction opaque-cell marker.
func MarkerForCell(hash, kind string, size int) string {
	return fmt.Sprintf("%s%s,%s,%d%s", markerPrefix, hash, kind, size, markerSuffix)
}

// MarkerForLossy builds a lossy row-drop marker.
func MarkerForLossy(hash string, rows int) string {
	return fmt.Sprintf("%s%s %d_rows_offloaded%s", markerPrefix, hash, rows, markerSuffix)
}

// ParseMarker extracts the hash from a canonical marker. Cell/lossy markers are
// parsed by their own consumers; this returns ok=false for them.
func ParseMarker(s string) (hash string, ok bool) {
	m := canonicalRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// hashesRe matches every retrieval hash a compressed block can name. Two
// surfaces exist and are intentionally not unified: the canonical <<ccr:HASH>>
// markers the live-zone dispatcher writes (in all three shapes), and the
// "hash=HEX" form the heuristic compressors and offload transforms write
// inline for upstream parity. Both keys are exactly 24 lowercase hex chars.
var hashesRe = regexp.MustCompile(`(?:<<ccr:|hash=)([0-9a-f]{24})`)

// HashesIn returns every retrieval hash the text names, deduped, in the order
// they appear.
//
// Callers use it to answer one question: can the model dereference everything
// this block is about to put on the wire? Checking only the canonical marker
// cannot answer it, because an accepted block carries both surfaces.
func HashesIn(s string) []string {
	ms := hashesRe.FindAllStringSubmatch(s, -1)
	if ms == nil {
		return nil
	}
	out := make([]string, 0, len(ms))
	seen := make(map[string]bool, len(ms))
	for _, m := range ms {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
