package ccr

import (
	"crypto/md5"
	"encoding/hex"
)

// ComputeKeyMD5 returns the first 24 lowercase hex chars of the MD5 of payload.
// The heuristic compressors (log/diff/search/diff_noise/json_offload) key their
// CCR originals this way to match upstream headroom (hashlib.md5(...).hexdigest()[:24]).
// The canonical live-zone marker uses ComputeKey (BLAKE3); these are intentionally distinct.
func ComputeKeyMD5(payload []byte) string {
	sum := md5.Sum(payload)
	return hex.EncodeToString(sum[:])[:24]
}
