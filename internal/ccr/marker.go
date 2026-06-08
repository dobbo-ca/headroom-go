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
