// Package livezone compresses the live zone of an Anthropic /v1/messages
// request body — the latest user message — while leaving every other byte
// exactly as it arrived.
//
// The dispatcher performs byte-range surgery. It locates the byte offsets of
// the JSON string values it may rewrite, compresses those strings, and
// splices the results back into the original buffer. It never deserialises,
// mutates and re-serialises the body, because doing so would disturb
// whitespace, key order, and numeric formatting the provider may have keyed
// its prompt cache on.
package livezone

import (
	"github.com/dobbo-ca/headroom-go/internal/cachecontrol"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/tidwall/gjson"
)

// BlockByteThreshold is the minimum decoded length of a block's text before
// the dispatcher will consider compressing it. Below this, the compressor's
// own overhead reliably exceeds anything it can save.
const BlockByteThreshold = 512

// DefaultModel selects the tokenizer when Options.Tokenizer is nil.
const DefaultModel = "claude-3-5-sonnet-20241022"

// HotZoneBlockTypes are block types treated as cache-hot even when they
// appear inside a live-zone message. Listed explicitly rather than matched
// by prefix so the cache-safety surface stays greppable.
var HotZoneBlockTypes = []string{
	"tool_use",
	"thinking",
	"redacted_thinking",
	// Anthropic compaction items are as sticky to the cache as tool_use
	// once injected.
	"compaction",
}

// Range is one rewritten span. Start and End index the INPUT body; NewLen is
// the byte length of what replaced it in the OUTPUT body. Callers reconstruct
// the untouched bytes by walking both buffers in lockstep.
type Range struct {
	Start  int
	End    int
	NewLen int
}

// Reason names why the dispatcher produced the body it did.
type Reason string

const (
	// ReasonOK means at least one block was rewritten.
	ReasonOK Reason = "ok"
	// ReasonNotJSON means the body is not a JSON object.
	ReasonNotJSON Reason = "not_json"
	// ReasonNoMessages means messages is missing or not an array.
	ReasonNoMessages Reason = "no_messages"
	// ReasonNoLiveZone means no user message sits at or above the frozen floor.
	ReasonNoLiveZone Reason = "no_live_zone"
	// ReasonNoCandidates means the live zone held no compressible block.
	ReasonNoCandidates Reason = "no_candidates"
	// ReasonAllRejected means every candidate failed the I5 token gate.
	ReasonAllRejected Reason = "all_rejected"
)

// Options configure one Dispatch call.
type Options struct {
	// Policy is the resolved per-auth-mode policy.
	Policy policy.CompressionPolicy
	// Router compresses one block's text. A nil Router disables
	// compression entirely and the body is forwarded verbatim.
	Router *router.Router
	// Store receives each rewritten block's original text. A nil Store
	// disables CCR marker injection.
	Store ccr.Store
	// Tokenizer counts tokens for the I5 gate. Nil selects DefaultModel.
	Tokenizer tokenizer.Tokenizer
	// FrozenCount is the number of leading messages that are cache-hot.
	// Pass -1 to derive it from the body's cache_control markers.
	FrozenCount int
	// Query biases relevance scoring inside the compressors.
	Query string
}

// BlockOutcome records what happened to one content block.
type BlockOutcome struct {
	Index        int
	BlockType    string
	Action       string // "compressed","hot_zone","below_threshold","no_op","rejected_tokens"
	Strategy     string
	TokensBefore int
	TokensAfter  int
	CacheKey     string
}

// Result is the outcome of a Dispatch. Body is never nil for a non-nil
// input, and on every bail-out path it is the input slice itself — so a
// caller may forward Body unconditionally without inspecting Reason.
type Result struct {
	Body         []byte
	Applied      bool
	Reason       Reason
	FrozenCount  int
	Rewritten    []Range
	Blocks       []BlockOutcome
	TokensBefore int
	TokensAfter  int
}

// passthrough builds the no-change result for a bail-out path.
func passthrough(body []byte, reason Reason, frozen int) Result {
	return Result{Body: body, Applied: false, Reason: reason, FrozenCount: frozen}
}

// Dispatch compresses the live zone of an Anthropic request body. It never
// returns an error: every failure mode forwards the original bytes.
func Dispatch(body []byte, opts Options) Result {
	if !gjson.ValidBytes(body) {
		return passthrough(body, ReasonNotJSON, 0)
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return passthrough(body, ReasonNotJSON, 0)
	}
	if !root.Get("messages").IsArray() {
		return passthrough(body, ReasonNoMessages, 0)
	}

	frozen := opts.FrozenCount
	if frozen < 0 {
		frozen, _ = cachecontrol.ComputeFrozenCount(body)
	}

	bodyStr := string(body)
	msgIdx, ok := findLatestUserMessage(bodyStr, frozen)
	if !ok {
		return passthrough(body, ReasonNoLiveZone, frozen)
	}

	slots := planBlocks(bodyStr, msgIdx)
	var candidates int
	for _, s := range slots {
		if s.kind == slotCompressible || s.kind == slotStringContent {
			candidates++
		}
	}
	if candidates == 0 {
		return passthrough(body, ReasonNoCandidates, frozen)
	}

	// Task 6 compresses the candidates and Task 7 splices them. Until then
	// the body is forwarded verbatim, which is what the I1 round-trip test
	// in roundtrip_test.go pins.
	return passthrough(body, ReasonNoCandidates, frozen)
}
