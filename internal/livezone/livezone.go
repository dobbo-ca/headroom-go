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
	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/dobbo-ca/headroom-go/internal/toolpairs"
	"github.com/dobbo-ca/headroom-go/internal/transform"
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
	// Store receives each rewritten block's original text. The router's
	// offload transforms stash the original unconditionally to build a
	// resolvable CCR marker, so a nil Store disables compression entirely
	// (every block is forwarded verbatim, like a nil Router).
	Store ccr.Store
	// Tokenizer counts tokens for the I5 gate. Nil selects DefaultModel.
	Tokenizer tokenizer.Tokenizer
	// FrozenCount is the number of leading messages that are cache-hot.
	// Pass -1 to derive it from the body's cache_control markers.
	FrozenCount int
	// Query biases relevance scoring inside the compressors.
	Query string
	// Replay re-applies a previous turn's compression to any block this
	// session has compressed before, ANYWHERE in the body — including deep
	// inside the cache-frozen prefix.
	//
	// That is safe only because it reproduces byte-for-byte what the
	// provider already cached, NOT because the frozen zone became
	// writable. Without it, compressing a block the client re-sends every
	// turn costs a cache miss per turn: the client re-sends the ORIGINAL,
	// which no longer matches the compressed prefix the provider stored,
	// so the hit truncates at that block and everything after it is
	// re-written at full price.
	//
	// Nil disables replay and Dispatch behaves as it did before.
	Replay *cachestab.SessionReplay
}

// BlockOutcome records what happened to one content block.
type BlockOutcome struct {
	// MessageIndex is the block's message index. The replay pass walks the
	// whole conversation, so Index alone no longer identifies a block.
	MessageIndex int
	Index        int
	BlockType    string
	Action       string // "compressed","replayed","hot_zone","below_threshold","no_op","rejected_tokens"
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

// blockContext builds the per-block compression context, resolving the block's
// tool_use_id to the tool that produced it. An unresolvable id yields an empty
// producer, which every gate treats as "unknown, do not assume it is safe".
func blockContext(opts Options, tools map[string]toolpairs.ToolUse, s planSlot) transform.CompressionContext {
	ctx := transform.CompressionContext{Query: opts.Query}
	if s.toolUseID == "" {
		return ctx
	}
	u, ok := tools[s.toolUseID]
	if !ok {
		return ctx
	}
	ctx.ProducingTool = u.Name
	ctx.ToolCommand = gjson.Get(u.Input, "command").String()
	return ctx
}

// storeResolves reports whether every retrieval marker in replacement can be
// read back out of the store, and whether the canonical hash still holds the
// exact original.
//
// It is the only check that answers the question a marker asks. Store.Put
// reports nothing, so a full disk, a revoked file or a capacity eviction is
// silent; and an accepted block carries TWO marker surfaces — the
// dispatcher's <<ccr:HASH>> under ComputeKey and the compressor's inline
// hash= under ComputeKeyMD5 — so checking one proves nothing about the other.
func storeResolves(store ccr.Store, hash, original, replacement string) bool {
	if got, ok := store.Get(hash); !ok || got != original {
		return false
	}
	for _, h := range ccr.HashesIn(replacement) {
		if _, ok := store.Get(h); !ok {
			return false
		}
	}
	return true
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

	tok := opts.Tokenizer
	if tok == nil {
		tok = tokenizer.GetTokenizer(DefaultModel)
	}

	// The producing tool is resolved from a single whole-body index. A
	// transform cannot tell raw file content from derived output without it,
	// and nothing inside a tool_result says which tool produced it.
	tools := toolpairs.Index(body)

	// Replay runs FIRST and over the whole conversation. A block this
	// session already compressed must be reproduced wherever it now sits,
	// or the prefix the provider cached no longer matches what arrives.
	// The fresh pass below skips anything replay already claimed.
	reps, outcomes, before, after := replayAll(bodyStr, root, opts, tok)
	claimed := make(map[int]bool, len(reps))
	for _, r := range reps {
		claimed[r.start] = true
	}

	msgIdx, liveOK := findLatestUserMessage(bodyStr, frozen)
	var slots []planSlot
	if liveOK {
		slots = planBlocks(bodyStr, msgIdx)
	}

	candidate := false
	for _, s := range slots {
		if claimed[s.start] {
			continue
		}
		switch s.kind {
		case slotHotZone:
			outcomes = append(outcomes, BlockOutcome{
				MessageIndex: msgIdx, Index: s.blockIndex, BlockType: s.blockType, Action: "hot_zone"})
			continue
		case slotBelowThreshold:
			outcomes = append(outcomes, BlockOutcome{
				MessageIndex: msgIdx, Index: s.blockIndex, BlockType: s.blockType, Action: "below_threshold"})
			continue
		}

		br := compressBlock(s.text, blockContext(opts, tools, s), opts, tok)
		outcomes = append(outcomes, BlockOutcome{
			MessageIndex: msgIdx,
			Index:        s.blockIndex,
			BlockType:    s.blockType,
			Action:       br.action,
			Strategy:     br.strategy,
			TokensBefore: br.tokensBefore,
			TokensAfter:  br.tokensAfter,
			CacheKey:     br.cacheKey,
		})
		if br.action == "rejected_tokens" {
			// ReasonAllRejected is reserved for blocks that actually failed
			// the I5 token gate. A "no_op" (no Router wired, or the router
			// returned the input unchanged) never attempted compression, so
			// it must not count toward that reason.
			candidate = true
		}
		if !br.accepted {
			continue
		}

		// The original is stored only now, after the gate accepted, so a
		// rejected block never leaves an orphan CCR entry. compressBlock
		// stages the router's own writes for the same reason.
		//
		// The store's Put reports nothing, so a full disk, a revoked file
		// or an eviction is silent. Read the entry back before putting its
		// marker on the wire: a marker the model cannot dereference is
		// worse than the bytes it replaced, and with replay on it stays
		// there for the whole session.
		if opts.Store != nil && br.cacheKey != "" {
			opts.Store.Put(br.cacheKey, s.text)
			if !storeResolves(opts.Store, br.cacheKey, s.text, br.replacement) {
				outcomes[len(outcomes)-1].Action = "store_unresolvable"
				continue
			}
			// Every later turn in this session must reproduce these exact
			// bytes for this block, or compressing it here costs a cache
			// miss per turn instead of saving anything.
			if opts.Replay != nil {
				opts.Replay.Record(br.cacheKey, br.replacement)
			}
		}
		reps = append(reps, replacement{
			start: s.start, end: s.end, repl: encodeJSONString(br.replacement)})
		before += br.tokensBefore
		after += br.tokensAfter
	}

	if len(reps) == 0 {
		switch {
		case !liveOK:
			return passthrough(body, ReasonNoLiveZone, frozen)
		case len(slots) == 0:
			return passthrough(body, ReasonNoCandidates, frozen)
		}
		reason := ReasonNoCandidates
		if candidate {
			reason = ReasonAllRejected
		}
		return Result{Body: body, Applied: false, Reason: reason, FrozenCount: frozen, Blocks: outcomes}
	}

	out, ranges := applyReplacements(body, reps)
	return Result{
		Body:         out,
		Applied:      true,
		Reason:       ReasonOK,
		FrozenCount:  frozen,
		Rewritten:    ranges,
		Blocks:       outcomes,
		TokensBefore: before,
		TokensAfter:  after,
	}
}
