// Package toolpairs encodes the structural atomicity invariants that keep an
// assistant tool_use block paired with its tool_result block across compression.
//
// Core finding (from upstream headroom live_zone.rs): atomicity is STRUCTURAL,
// not key-based. Upstream never reads tool_use_id to match a tool_use block to a
// tool_result block; there is zero cross-message id-matching code. The pair is
// kept intact by construction:
//
//  1. tool_use is hot-zone-excluded and never rewritten (HotZoneBlockTypes).
//  2. a tool_result is compressed only within its inner content field, leaving
//     its type/tool_use_id/sibling keys byte-identical (InnerContentField).
//
// This package exposes exactly those two facts for the future (v0.2) live-zone
// dispatcher. It deliberately does NOT implement a tool_use_id matcher — adding
// one would diverge from upstream behavior.
package toolpairs

// hotZoneBlockTypes is the exact, ordered list of block types that must never be
// rewritten, because mutating one would split a tool pair or bust the prompt
// cache. Matched by exact string equality only — no prefix, substring, or case
// folding. (Upstream: HOT_ZONE_BLOCK_TYPES, any(|t| *t == block_type).)
var hotZoneBlockTypes = []string{"tool_use", "thinking", "redacted_thinking", "compaction"}

// HotZoneBlockTypes returns the exact, ordered hot-zone block-type list so
// callers and tests share one source of truth. The returned slice is a copy;
// mutating it does not affect the package's list.
func HotZoneBlockTypes() []string {
	out := make([]string, len(hotZoneBlockTypes))
	copy(out, hotZoneBlockTypes)
	return out
}

// IsHotZoneBlockType reports whether blockType is a hot-zone type that must never
// be rewritten. The comparison is exact equality — no prefix matching, no case
// folding, no trimming.
func IsHotZoneBlockType(blockType string) bool {
	for _, t := range hotZoneBlockTypes {
		if t == blockType {
			return true
		}
	}
	return false
}

// InnerContentField returns the name of the single compressible inner field for
// a block type, and whether one exists: "tool_result" -> ("content", true);
// "text" -> ("text", true); anything else -> ("", false).
//
// This encodes the compress-inner-content-only contract: the dispatcher rewrites
// only the named inner field's value and never the block envelope or its
// pairing id. Block types with no compressible inner field (image, document,
// the "unknown" default, structured-array content, etc.) return ("", false) and
// are left untouched.
func InnerContentField(blockType string) (field string, ok bool) {
	switch blockType {
	case "tool_result":
		return "content", true
	case "text":
		return "text", true
	default:
		return "", false
	}
}
