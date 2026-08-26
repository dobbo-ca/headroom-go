package livezone

import (
	"fmt"
	"slices"

	"github.com/tidwall/gjson"
)

// slotKind classifies one content block's disposition.
type slotKind int

const (
	// slotCompressible is a JSON string the dispatcher may rewrite.
	slotCompressible slotKind = iota
	// slotStringContent is the Anthropic legacy shape: the whole message's
	// content is a JSON string with no block array.
	slotStringContent
	// slotHotZone is a cache-hot block type: recorded, never dispatched.
	slotHotZone
	// slotBelowThreshold is a candidate too short to be worth compressing.
	slotBelowThreshold
	// slotImage is an image block's base64 payload. Never routed through the
	// compressors: the router would classify base64 as PlainText and hand it to
	// TextOffload.
	slotImage
	// slotUnreachable is a block or array element whose type the dispatcher has
	// no compressible field for. Recorded so a harness can tell "I saw this and
	// could not act on it" from "there was nothing here".
	slotUnreachable
)

// planSlot is one block's plan entry. start and end bracket the JSON string
// value in the body INCLUDING its enclosing quotes, so a replacement written
// over [start,end) is itself a complete JSON string value.
type planSlot struct {
	blockIndex int
	kind       slotKind
	blockType  string
	text       string
	start      int
	end        int
	// toolUseID links a tool_result back to the tool_use that produced it.
	// Empty for any other block type. The dispatcher resolves it against a
	// whole-body index to learn the PRODUCING TOOL, which is the only way a
	// transform can tell raw file content from derived output.
	toolUseID string
	// contentIndex is the slot's index inside a tool_result's structured content
	// array, or -1 when the slot is not nested. blockIndex stays the index of the
	// block in the MESSAGE's content array either way, so a nested slot needs both
	// numbers to be identified.
	contentIndex int
}

// isHotZone reports whether a block type is cache-hot.
func isHotZone(blockType string) bool {
	return slices.Contains(HotZoneBlockTypes, blockType)
}

// findLatestUserMessage returns the index of the highest-numbered message
// with role "user" at or above floor. Messages below floor are frozen.
func findLatestUserMessage(body string, floor int) (int, bool) {
	messages := gjson.Get(body, "messages")
	if !messages.IsArray() {
		return 0, false
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= floor; i-- {
		if arr[i].Get("role").String() == "user" {
			return i, true
		}
	}
	return 0, false
}

// stringSlot builds a slot from a gjson result that must be a JSON string.
// It returns ok=false when the result is not a string or when gjson could
// not report a byte offset.
//
// The Index == 0 guard is load-bearing: gjson uses 0 to mean "offset
// unknown", and no nested value can legitimately sit at offset 0 (that is
// the body's opening brace). Rewriting at a bogus offset would corrupt the
// frozen prefix, so an unknown offset must drop the slot.
func stringSlot(r gjson.Result) (start, end int, text string, ok bool) {
	if r.Type != gjson.String || r.Index <= 0 {
		return 0, 0, "", false
	}
	return r.Index, r.Index + len(r.Raw), r.Str, true
}

// classify turns a located string into a slot of the right kind, applying
// the byte-threshold gate.
func classify(blockIndex int, blockType string, start, end int, text string, base slotKind) planSlot {
	kind := base
	if len(text) < BlockByteThreshold {
		kind = slotBelowThreshold
	}
	return planSlot{
		blockIndex:   blockIndex,
		kind:         kind,
		blockType:    blockType,
		text:         text,
		start:        start,
		end:          end,
		contentIndex: -1,
	}
}

// planBlocks walks the target message and returns one slot per block it
// recognised. Blocks it does not recognise are omitted entirely.
//
// Every gjson lookup runs against the full body so Result.Index is an
// absolute offset; a lookup against a sub-slice would return offsets
// relative to that slice and silently corrupt the splice.
func planBlocks(body string, msgIdx int) []planSlot {
	content := gjson.Get(body, fmt.Sprintf("messages.%d.content", msgIdx))

	if content.Type == gjson.String {
		start, end, text, ok := stringSlot(content)
		if !ok {
			return nil
		}
		return []planSlot{classify(0, "", start, end, text, slotStringContent)}
	}

	if !content.IsArray() {
		return nil
	}

	var slots []planSlot
	for i, block := range content.Array() {
		blockType := block.Get("type").String()

		if isHotZone(blockType) {
			slots = append(slots, planSlot{blockIndex: i, kind: slotHotZone, blockType: blockType, contentIndex: -1})
			continue
		}

		var field string
		switch blockType {
		case "text":
			field = "text"
		case "image":
			field = "source.data"
		case "tool_result":
			// Check if content is a structured array
			inner := gjson.Get(body, fmt.Sprintf("messages.%d.content.%d.content", msgIdx, i))
			if inner.IsArray() {
				// Walk the nested array
				toolUseID := block.Get("tool_use_id").String()
				for j, elem := range inner.Array() {
					elemType := elem.Get("type").String()
					// Nested cache_control markers are hot-zone: the frozen floor
					// computation only walks top-level content, but a nested marker
					// must be treated as frozen to preserve the cache key.
					if elem.Get("cache_control").Exists() || isHotZone(elemType) {
						slots = append(slots, planSlot{
							blockIndex:   i,
							kind:         slotHotZone,
							blockType:    elemType,
							toolUseID:    toolUseID,
							contentIndex: j,
						})
						continue
					}
					var nestedField string
					switch elemType {
					case "text":
						nestedField = fmt.Sprintf("messages.%d.content.%d.content.%d.text", msgIdx, i, j)
					case "image":
						nestedField = fmt.Sprintf("messages.%d.content.%d.content.%d.source.data", msgIdx, i, j)
					default:
						// Unrecognized nested type
						slots = append(slots, planSlot{
							blockIndex:   i,
							kind:         slotUnreachable,
							blockType:    elemType,
							toolUseID:    toolUseID,
							contentIndex: j,
						})
						continue
					}
					r := gjson.Get(body, nestedField)
					start, end, text, ok := stringSlot(r)
					if !ok {
						// Missing or non-string field: record as unreachable
						// so the harness can distinguish "saw it, no valid field"
						// from "block was not scanned at all".
						slots = append(slots, planSlot{
							blockIndex:   i,
							kind:         slotUnreachable,
							blockType:    elemType,
							toolUseID:    toolUseID,
							contentIndex: j,
						})
						continue
					}
					kind := slotCompressible
					if elemType == "image" {
						kind = slotImage
					}
					slot := classify(i, elemType, start, end, text, kind)
					slot.toolUseID = toolUseID
					slot.contentIndex = j
					slots = append(slots, slot)
				}
				continue
			}
			// String content
			field = "content"
		default:
			// Unrecognized top-level type
			slots = append(slots, planSlot{blockIndex: i, kind: slotUnreachable, blockType: blockType, contentIndex: -1})
			continue
		}

		r := gjson.Get(body, fmt.Sprintf("messages.%d.content.%d.%s", msgIdx, i, field))
		start, end, text, ok := stringSlot(r)
		if !ok {
			// Missing or non-string field
			slots = append(slots, planSlot{blockIndex: i, kind: slotUnreachable, blockType: blockType, contentIndex: -1})
			continue
		}
		kind := slotCompressible
		if blockType == "image" {
			kind = slotImage
		}
		slot := classify(i, blockType, start, end, text, kind)
		if blockType == "tool_result" {
			slot.toolUseID = block.Get("tool_use_id").String()
		}
		slots = append(slots, slot)
	}
	return slots
}
