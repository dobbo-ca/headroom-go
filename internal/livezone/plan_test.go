package livezone

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// big builds a decoded string of exactly n bytes, so threshold boundaries can
// be pinned exactly.
func big(n int) string { return strings.Repeat("x", n) }

func TestFindLatestUserMessage(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"a"},
		{"role":"assistant","content":"b"},
		{"role":"user","content":"c"},
		{"role":"assistant","content":"d"}]}`

	tests := []struct {
		name   string
		floor  int
		want   int
		wantOK bool
	}{
		{"no floor picks the last user message", 0, 2, true},
		// The floor is inclusive of what is FROZEN: with floor 2, index 2 is
		// the first live index and is still eligible.
		{"floor at the target keeps it eligible", 2, 2, true},
		// With floor 3, the only user messages are below the floor.
		{"floor above the last user message finds none", 3, 0, false},
		{"floor beyond the array finds none", 99, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := findLatestUserMessage(body, tt.floor)
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Errorf("findLatestUserMessage(floor=%d) = (%d,%v), want (%d,%v)",
					tt.floor, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestFindLatestUserMessageNoUserRole(t *testing.T) {
	body := `{"messages":[{"role":"assistant","content":"a"},{"role":"system","content":"b"}]}`
	if _, ok := findLatestUserMessage(body, 0); ok {
		t.Error("expected no user message")
	}
}

// Every planned slot's byte range must slice out of the body exactly the raw
// JSON string it claims to cover. This is the property the splice depends on.
func TestPlanSlotRangesSliceBackExactly(t *testing.T) {
	text := big(600)
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"` + text + `"}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	s := slots[0]
	if s.kind != slotCompressible {
		t.Fatalf("kind = %v, want slotCompressible", s.kind)
	}
	if s.text != text {
		t.Errorf("decoded text length %d, want %d", len(s.text), len(text))
	}
	raw := body[s.start:s.end]
	if raw != `"`+text+`"` {
		t.Errorf("range [%d,%d) did not slice out the quoted string value", s.start, s.end)
	}
	// The range must include both quotes, never the surrounding JSON.
	if raw[0] != '"' || raw[len(raw)-1] != '"' {
		t.Errorf("range must include the enclosing quotes, got %q...%q", raw[0], raw[len(raw)-1])
	}
}

func TestPlanBlocksClassifiesTypes(t *testing.T) {
	long := big(600)
	body := `{"messages":[{"role":"user","content":[` +
		`{"type":"thinking","thinking":"` + long + `"},` +
		`{"type":"tool_use","id":"t","name":"n","input":{}},` +
		`{"type":"redacted_thinking","data":"` + long + `"},` +
		`{"type":"compaction","text":"` + long + `"},` +
		`{"type":"text","text":"` + long + `"},` +
		`{"type":"tool_result","tool_use_id":"t","content":"` + long + `"},` +
		`{"type":"image","source":{"data":"` + long + `"}},` +
		`{"type":"text","text":"short"}` +
		`]}]}`

	slots := planBlocks(body, 0)
	byIndex := map[int]planSlot{}
	for _, s := range slots {
		byIndex[s.blockIndex] = s
	}

	// The four hot-zone types are recorded, never dispatched.
	for _, i := range []int{0, 1, 2, 3} {
		s, ok := byIndex[i]
		if !ok {
			t.Fatalf("block %d missing from the plan", i)
		}
		if s.kind != slotHotZone {
			t.Errorf("block %d kind = %v, want slotHotZone (type %q)", i, s.kind, s.blockType)
		}
	}
	if s := byIndex[4]; s.kind != slotCompressible || s.text != long {
		t.Errorf("text block: kind = %v, len(text) = %d", s.kind, len(s.text))
	}
	if s := byIndex[5]; s.kind != slotCompressible || s.text != long {
		t.Errorf("tool_result block: kind = %v, len(text) = %d", s.kind, len(s.text))
	}
	// Image blocks are now recognized and planned as slotImage.
	if s := byIndex[6]; s.kind != slotImage || s.text != long {
		t.Errorf("image block: kind = %v, len(text) = %d, want slotImage", s.kind, len(s.text))
	}
	if s := byIndex[7]; s.kind != slotBelowThreshold {
		t.Errorf("short text block kind = %v, want slotBelowThreshold", s.kind)
	}
}

// The gate is >= BlockByteThreshold. One byte either side must land on
// opposite sides of it.
func TestPlanBlocksThresholdBoundary(t *testing.T) {
	tests := []struct {
		n    int
		want slotKind
	}{
		{BlockByteThreshold - 1, slotBelowThreshold},
		{BlockByteThreshold, slotCompressible},
		{BlockByteThreshold + 1, slotCompressible},
	}
	for _, tt := range tests {
		body := `{"messages":[{"role":"user","content":[{"type":"text","text":"` + big(tt.n) + `"}]}]}`
		slots := planBlocks(body, 0)
		if len(slots) != 1 {
			t.Fatalf("n=%d: got %d slots, want 1", tt.n, len(slots))
		}
		if slots[0].kind != tt.want {
			t.Errorf("n=%d: kind = %v, want %v", tt.n, slots[0].kind, tt.want)
		}
	}
}

func TestPlanBlocksStringContent(t *testing.T) {
	text := big(600)
	body := `{"messages":[{"role":"user","content":"` + text + `"}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].kind != slotStringContent {
		t.Errorf("kind = %v, want slotStringContent", slots[0].kind)
	}
	if body[slots[0].start:slots[0].end] != `"`+text+`"` {
		t.Error("string-content range did not slice back exactly")
	}
}

// Escaped content must decode correctly AND its byte range must still cover
// the RAW (escaped) form, which is longer than the decoded string.
func TestPlanBlocksEscapedTextRangeCoversRawForm(t *testing.T) {
	decoded := strings.Repeat(`a"b\c`, 200) // 1000 decoded bytes, more raw
	escaped := strings.Repeat(`a\"b\\c`, 200)
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"` + escaped + `"}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 || slots[0].kind != slotCompressible {
		t.Fatalf("got %d slots, kind %v", len(slots), slots[0].kind)
	}
	if slots[0].text != decoded {
		t.Errorf("decoded text mismatch: got %d bytes, want %d", len(slots[0].text), len(decoded))
	}
	if body[slots[0].start:slots[0].end] != `"`+escaped+`"` {
		t.Error("range must cover the raw escaped form, not the decoded form")
	}
}

func TestPlanBlocksEmptyAndMissingContent(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user"}]}`,
		`{"messages":[{"role":"user","content":[]}]}`,
		`{"messages":[{"role":"user","content":null}]}`,
		`{"messages":[{"role":"user","content":123}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			if slots := planBlocks(body, 0); len(slots) != 0 {
				t.Errorf("got %d slots, want 0", len(slots))
			}
		})
	}
}

func TestPlanBlocksOutOfRangeMessageIndex(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"a"}]}`
	if slots := planBlocks(body, 5); len(slots) != 0 {
		t.Errorf("got %d slots for an out-of-range index, want 0", len(slots))
	}
}

// gjson reports Index 0 for a value whose offset it could not determine.
// stringSlot must drop such a result: rewriting at offset 0 would overwrite
// the body's opening brace and destroy the frozen prefix.
func TestStringSlotRejectsUnknownOffset(t *testing.T) {
	tests := []struct {
		name   string
		in     gjson.Result
		wantOK bool
	}{
		{"unknown offset is dropped",
			gjson.Result{Type: gjson.String, Index: 0, Raw: `"abc"`, Str: "abc"}, false},
		{"non string is dropped",
			gjson.Result{Type: gjson.Number, Index: 10, Raw: `123`}, false},
		{"located string is kept",
			gjson.Result{Type: gjson.String, Index: 10, Raw: `"abc"`, Str: "abc"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, text, ok := stringSlot(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				if start != 0 || end != 0 || text != "" {
					t.Errorf("rejected slot must be zeroed, got (%d,%d,%q)", start, end, text)
				}
				return
			}
			if start != tt.in.Index || end != tt.in.Index+len(tt.in.Raw) || text != tt.in.Str {
				t.Errorf("got (%d,%d,%q), want (%d,%d,%q)",
					start, end, text, tt.in.Index, tt.in.Index+len(tt.in.Raw), tt.in.Str)
			}
		})
	}
}

// The threshold gate applies to string content too: a short legacy-shape
// message is not a candidate.
func TestPlanBlocksStringContentBelowThreshold(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"` + big(BlockByteThreshold-1) + `"}]}`
	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].kind != slotBelowThreshold {
		t.Errorf("kind = %v, want slotBelowThreshold", slots[0].kind)
	}
}

// The gate measures the DECODED text, not the raw byte span. A heavily
// escaped block can occupy far more raw bytes than it decodes to, and the
// compressor only ever sees the decoded form.
func TestPlanBlocksThresholdMeasuresDecodedText(t *testing.T) {
	decoded := strings.Repeat(`"`, 300)  // 300 decoded bytes
	escaped := strings.Repeat(`\"`, 300) // 600 raw bytes
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"` + escaped + `"}]}]}`

	slots := planBlocks(body, 0)
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].text != decoded {
		t.Fatalf("decoded text = %d bytes, want %d", len(slots[0].text), len(decoded))
	}
	if got := slots[0].end - slots[0].start; got <= BlockByteThreshold {
		t.Fatalf("raw span %d must exceed the threshold for this test to bite", got)
	}
	if slots[0].kind != slotBelowThreshold {
		t.Errorf("kind = %v, want slotBelowThreshold: the gate must use the decoded length", slots[0].kind)
	}
}
