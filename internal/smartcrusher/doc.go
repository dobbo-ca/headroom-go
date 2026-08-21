// Package smartcrusher implements the SmartCrusher MVP: the JSON-array
// compressor that backs offloads.JsonOffload. It is a clean-room Go port of
// upstream chopratejas/headroom's crates/headroom-core/src/transforms/smart_crusher/*.
//
// The package decomposes 1:1 with the upstream Rust submodules — one concern per
// Go file — and reuses the existing internal/ccr helpers (Store, ComputeKey,
// ComputeKeyMD5, the three marker builders) and internal/relevance (Scorer,
// NewHybridScorer). smartcrusher.SmartCrusher implements the offloads.Crusher
// seam; internally it carries a richer 4-field types.CrushResult, and the seam
// boundary maps crushed->Compressed / wasModified->WasModified while dropping
// Strategy/Original. All JSON objects on the compression path are decoded with
// github.com/iancoleman/orderedmap: map[string]any is BANNED because key
// insertion/parse order is load-bearing throughout.
//
// Decision B — NO byte-parity. Upstream's stated goal is byte-exact parity with
// a Python reference; headroom-go REJECTS that goal. We capture behavior, ratio
// bounds, and marker/strategy SHAPES, not byte-exact compressed output. The four
// upstream Python bugs are ANTI-REQUIREMENTS — this port implements the CORRECT
// behavior instead:
//
//	BUG#1 percentile: proper linear-interpolation percentile, not int-div index.
//	BUG#2 sequential: zero-padded numeric strings (["001".."010"]) are categorical.
//	BUG#3 rare-status: no >10-distinct short-circuit; cardinality cap is 50.
//	BUG#4 k-split: kFirst+kLast clamped to <= kTotal (respects max_items_after_crush).
//
// The only byte-pinned identifiers we replicate exactly are enum String() values,
// ERROR_KEYWORDS, hash hex, and marker structural literals.
package smartcrusher
