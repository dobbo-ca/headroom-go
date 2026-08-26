package livezone

import (
	"strings"
	"testing"
)

// TestPlanBlocksWalksStructuredToolResultArray proves the dispatcher can see
// nested content. Before this, array content gave blocks=0; after, it returns
// one slot per element.
func TestPlanBlocksWalksStructuredToolResultArray(t *testing.T) {
	long := big(600)
	b64 := strings.Repeat("iVBORw0KGgo", 60) // 660 chars, above threshold
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"` + long + `"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 2 {
		t.Fatalf("got %d slots, want 2", len(slots))
	}

	// First nested element: text
	if slots[0].kind != slotCompressible {
		t.Errorf("nested text kind = %v, want slotCompressible", slots[0].kind)
	}
	if slots[0].blockType != "text" {
		t.Errorf("nested text blockType = %q, want %q", slots[0].blockType, "text")
	}
	if slots[0].contentIndex != 0 {
		t.Errorf("nested text contentIndex = %d, want 0", slots[0].contentIndex)
	}
	if slots[0].text != long {
		t.Errorf("nested text length %d, want %d", len(slots[0].text), len(long))
	}

	// Second nested element: image
	if slots[1].kind != slotImage {
		t.Errorf("nested image kind = %v, want slotImage", slots[1].kind)
	}
	if slots[1].blockType != "image" {
		t.Errorf("nested image blockType = %q, want %q", slots[1].blockType, "image")
	}
	if slots[1].contentIndex != 1 {
		t.Errorf("nested image contentIndex = %d, want 1", slots[1].contentIndex)
	}
}

// TestNestedSlotRangesSliceBackExactly asserts body[s.start:s.end] equals the
// quoted JSON string for both nested text and nested image, on the first and
// last element of a 3-element array.
func TestNestedSlotRangesSliceBackExactly(t *testing.T) {
	text1 := big(600)
	text2 := big(700)
	b64 := strings.Repeat("abcd", 150) // 600 chars
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"` + text1 + `"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}},{"type":"text","text":"` + text2 + `"}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 3 {
		t.Fatalf("got %d slots, want 3", len(slots))
	}

	// First element: nested text
	s0 := slots[0]
	raw0 := body[s0.start:s0.end]
	if raw0 != `"`+text1+`"` {
		t.Errorf("nested text [0] range did not slice back the quoted string exactly")
	}

	// Second element: nested image
	s1 := slots[1]
	raw1 := body[s1.start:s1.end]
	if raw1 != `"`+b64+`"` {
		t.Errorf("nested image [1] range did not slice back the quoted base64 exactly")
	}

	// Third element: nested text
	s2 := slots[2]
	raw2 := body[s2.start:s2.end]
	if raw2 != `"`+text2+`"` {
		t.Errorf("nested text [2] range did not slice back the quoted string exactly")
	}
}

// TestNestedSlotsCarryDistinctContentIndex confirms contentIndex on a 3-element
// array is 0,1,2 and blockIndex is the outer index for all three.
func TestNestedSlotsCarryDistinctContentIndex(t *testing.T) {
	long := big(600)
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"` + long + `"},{"type":"text","text":"` + long + `"},{"type":"text","text":"` + long + `"}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 3 {
		t.Fatalf("got %d slots, want 3", len(slots))
	}

	for i, s := range slots {
		if s.blockIndex != 0 {
			t.Errorf("slot %d blockIndex = %d, want 0 (outer index)", i, s.blockIndex)
		}
		if s.contentIndex != i {
			t.Errorf("slot %d contentIndex = %d, want %d", i, s.contentIndex, i)
		}
	}
}

// TestNonNestedSlotsHaveContentIndexMinusOne confirms top-level text, string
// content, and hot-zone slots have contentIndex = -1.
func TestNonNestedSlotsHaveContentIndexMinusOne(t *testing.T) {
	long := big(600)
	tests := []struct {
		name string
		body string
		want slotKind
	}{
		{"top-level text", `{"messages":[{"role":"user","content":[{"type":"text","text":"` + long + `"}]}]}`, slotCompressible},
		{"string content", `{"messages":[{"role":"user","content":"` + long + `"}]}`, slotStringContent},
		{"string tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + long + `"}]}]}`, slotCompressible},
		{"hot zone", `{"messages":[{"role":"user","content":[{"type":"thinking","thinking":"` + long + `"}]}]}`, slotHotZone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := planBlocks(tt.body, 0)
			if len(slots) != 1 {
				t.Fatalf("got %d slots, want 1", len(slots))
			}
			if slots[0].kind != tt.want {
				t.Errorf("kind = %v, want %v", slots[0].kind, tt.want)
			}
			if slots[0].contentIndex != -1 {
				t.Errorf("contentIndex = %d, want -1", slots[0].contentIndex)
			}
		})
	}
}

// TestPlanBlocksNestedUnknownTypeIsUnreachableNotOmitted asserts an element
// type "document" produces slotUnreachable, by kind name.
func TestPlanBlocksNestedUnknownTypeIsUnreachableNotOmitted(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"document","data":"xyz"}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].kind != slotUnreachable {
		t.Errorf("kind = %v, want slotUnreachable", slots[0].kind)
	}
	if slots[0].blockType != "document" {
		t.Errorf("blockType = %q, want %q", slots[0].blockType, "document")
	}
}

// TestPlanBlocksTopLevelUnknownTypeIsUnreachable asserts the top-level default
// case produces slotUnreachable instead of being skipped.
func TestPlanBlocksTopLevelUnknownTypeIsUnreachable(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"unknown","data":"xyz"}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].kind != slotUnreachable {
		t.Errorf("kind = %v, want slotUnreachable", slots[0].kind)
	}
	if slots[0].blockType != "unknown" {
		t.Errorf("blockType = %q, want %q", slots[0].blockType, "unknown")
	}
}

// TestPlanBlocksNestedHotZoneTypeIsProtected asserts isHotZone on the element
// type: a "thinking" element becomes slotHotZone, not slotCompressible.
func TestPlanBlocksNestedHotZoneTypeIsProtected(t *testing.T) {
	long := big(600)
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"thinking","thinking":"` + long + `"}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].kind != slotHotZone {
		t.Errorf("nested thinking kind = %v, want slotHotZone", slots[0].kind)
	}
	if slots[0].blockType != "thinking" {
		t.Errorf("blockType = %q, want %q", slots[0].blockType, "thinking")
	}
}

// TestPlanBlocksNestedThresholdBoundary applies the threshold gate to nested
// text: BlockByteThreshold-1/+0/+1 must land on opposite sides.
func TestPlanBlocksNestedThresholdBoundary(t *testing.T) {
	tests := []struct {
		n    int
		want slotKind
	}{
		{BlockByteThreshold - 1, slotBelowThreshold},
		{BlockByteThreshold, slotCompressible},
		{BlockByteThreshold + 1, slotCompressible},
	}
	for _, tt := range tests {
		body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"` + big(tt.n) + `"}]}]}]}`
		slots := planBlocks(body, 0)
		if len(slots) != 1 {
			t.Fatalf("n=%d: got %d slots, want 1", tt.n, len(slots))
		}
		if slots[0].kind != tt.want {
			t.Errorf("n=%d: kind = %v, want %v", tt.n, slots[0].kind, tt.want)
		}
	}
}

// TestPlanBlocksNestedToolUseIDIsInherited confirms every nested slot carries
// the outer tool_use_id.
func TestPlanBlocksNestedToolUseIDIsInherited(t *testing.T) {
	long := big(600)
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t42","content":[{"type":"text","text":"` + long + `"},{"type":"text","text":"` + long + `"}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 2 {
		t.Fatalf("got %d slots, want 2", len(slots))
	}
	for i, s := range slots {
		if s.toolUseID != "t42" {
			t.Errorf("slot %d toolUseID = %q, want %q", i, s.toolUseID, "t42")
		}
	}
}

// TestPlanBlocksImageSlotSkipsNonBase64Source asserts source.type "url" or
// "file" produces no slot (no source.data field exists).
func TestPlanBlocksImageSlotSkipsNonBase64Source(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
	}{
		{"url", `"url","url":"https://example.com/image.png"`},
		{"file", `"file","path":"/tmp/image.png"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":` + tt.sourceType + `}}]}]}]}`
			slots := planBlocks(body, 0)
			// An image without source.data produces no slot
			if len(slots) != 0 {
				t.Errorf("got %d slots, want 0 (no source.data)", len(slots))
			}
		})
	}
}

// TestPlanBlocksNestedArrayOfOneImageOnly confirms the ('image',) shape produces
// exactly one slotImage and no stray outer slot.
func TestPlanBlocksNestedArrayOfOneImageOnly(t *testing.T) {
	b64 := strings.Repeat("abcd", 150)
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1 (only the nested image)", len(slots))
	}
	if slots[0].kind != slotImage {
		t.Errorf("kind = %v, want slotImage", slots[0].kind)
	}
	if slots[0].blockType != "image" {
		t.Errorf("blockType = %q, want %q", slots[0].blockType, "image")
	}
	if slots[0].contentIndex != 0 {
		t.Errorf("contentIndex = %d, want 0", slots[0].contentIndex)
	}
}
