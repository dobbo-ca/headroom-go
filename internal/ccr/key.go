// Package ccr implements reversible compression: originals are stashed in a
// Store under a short content key, and a marker is left in the compressed text
// so headroom_retrieve can recover the original on demand.
package ccr

import (
	"encoding/hex"

	"lukechampine.com/blake3"
)

// ComputeKey returns the first 24 lowercase hex chars (96 bits) of the BLAKE3
// hash of payload. Deterministic; collision-safe for CCR's working-set sizes.
func ComputeKey(payload []byte) string {
	sum := blake3.Sum256(payload)
	return hex.EncodeToString(sum[:12])
}
