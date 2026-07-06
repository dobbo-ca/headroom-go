package smartcrusher

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashFieldName returns the first 8 lowercase-hex chars of the SHA-256 digest of
// the field name's UTF-8 bytes. It mirrors Python
// hashlib.sha256(name.encode()).hexdigest()[:8] and is used as a cache key to
// look up TOIN-anonymized preserve_fields (TOIN stores names as SHA-256[:8]).
//
// This is NOT a CCR marker hash and never emits <<ccr:...>>. The truncation is
// load-bearing: it MUST be [:8] — a prior [:16] silently broke all preserve-field
// lookups. Hash the bytes, not the runes ("café" -> "850f7dc4")
// [ref: hashing.rs].
func HashFieldName(fieldName string) string {
	sum := sha256.Sum256([]byte(fieldName))
	return hex.EncodeToString(sum[:])[:8]
}
