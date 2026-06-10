package toolpairs

import "testing"

func TestHotZoneExactList(t *testing.T) {
	want := []string{"tool_use", "thinking", "redacted_thinking", "compaction"}
	got := HotZoneBlockTypes()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsHotZoneExactMatchOnly(t *testing.T) {
	if !IsHotZoneBlockType("tool_use") {
		t.Error("tool_use must be hot-zone")
	}
	if IsHotZoneBlockType("tool_used") || IsHotZoneBlockType("Tool_Use") {
		t.Error("must be exact-equality (no prefix/case folding)")
	}
}

func TestInnerContentField(t *testing.T) {
	if f, ok := InnerContentField("tool_result"); !ok || f != "content" {
		t.Errorf("tool_result -> %q,%v", f, ok)
	}
	if f, ok := InnerContentField("text"); !ok || f != "text" {
		t.Errorf("text -> %q,%v", f, ok)
	}
	if _, ok := InnerContentField("image"); ok {
		t.Error("other types have no compressible inner field")
	}
}
