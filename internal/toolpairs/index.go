package toolpairs

import (
	"github.com/tidwall/gjson"
)

// ToolUse is what produced a tool_result: the tool's name and the input it was
// called with.
type ToolUse struct {
	Name string
	// Input is the raw JSON of the tool_use input object, or "" when absent.
	// Kept raw so a caller reads only the field it needs and no re-encoding can
	// disturb the body.
	Input string
}

// Index maps tool_use_id to the tool_use that produced it, in ONE pass over the
// whole body — including the cache-frozen prefix, so a tool_result whose
// tool_use sits in an already-cached message still resolves.
//
// This is a deliberate divergence from the rest of this package, which
// documents that upstream keeps tool pairs intact STRUCTURALLY and never
// matches by id. That remains true of the Rust live-zone dispatcher. But
// upstream's Python interceptors DO build exactly this index
// (headroom/proxy/interceptors/base.py::_build_tool_use_index) because a
// transform cannot decide whether output is safe to compress without knowing
// which tool produced it: file-read output must not be lossy-compressed, and
// nothing in a tool_result says it came from a read.
//
// Verified on four real Claude Code request bodies: every tool_result's
// tool_use_id resolved to a tool_use in the same body, always in the
// immediately preceding assistant message. The API enforces the pairing, so
// this needs no cross-request state.
func Index(body []byte) map[string]ToolUse {
	out := map[string]ToolUse{}
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			id := block.Get("id").String()
			if id == "" {
				return true
			}
			out[id] = ToolUse{
				Name:  block.Get("name").String(),
				Input: block.Get("input").Raw,
			}
			return true
		})
		return true
	})
	return out
}
