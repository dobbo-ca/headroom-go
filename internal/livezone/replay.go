package livezone

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/tidwall/gjson"
)

// replayAll re-applies this session's earlier compressions across the WHOLE
// conversation, and returns the replacements, one outcome per replayed block,
// and the token counts those blocks moved.
//
// It deliberately ignores the frozen floor. A replayed block is identical to
// what the provider already cached, so rewriting it inside the frozen prefix
// preserves the cache key rather than breaking it; NOT rewriting it is what
// breaks the key, because the client always re-sends the original.
//
// Identity is the original text's ccr.ComputeKey — the same key the canonical
// <<ccr:HASH>> marker in the replayed text names, so a hit gives the store
// entry and the wire marker one shared identity.
//
// ponytail: one full-body gjson walk per message, so locating the blocks of an
// N-message body costs O(bodyLen * N). Measured 2026-08-25 on an M4 Pro at
// ~185 KB: 0.9 ms at 10 messages, 2.0 ms at 40, 3.4 ms at 80 — against a hop
// that is followed by a multi-second model call. Index every offset in one
// pass only if a profile says this has started to matter.
func replayAll(bodyStr string, root gjson.Result, opts Options, tok tokenizer.Tokenizer) ([]replacement, []BlockOutcome, int, int) {
	if opts.Replay == nil {
		return nil, nil, 0, 0
	}

	var (
		reps     []replacement
		outcomes []BlockOutcome
		before   int
		after    int
	)
	for msgIdx := range root.Get("messages").Array() {
		for _, s := range planBlocks(bodyStr, msgIdx) {
			if s.kind != slotCompressible && s.kind != slotStringContent {
				continue
			}
			hash := ccr.ComputeKey([]byte(s.text))
			compressed, ok := opts.Replay.Lookup(hash)
			if !ok {
				continue
			}

			// Re-store the original under the hash the replayed text's
			// marker names. The store's TTL is five minutes and a session
			// runs far longer, so a marker that stays on the wire would
			// otherwise outlive the entry it dereferences. Refreshing on
			// every replay keeps exactly the working set that is on the
			// wire alive, and no more — which is why the TTL itself does
			// not need to change.
			if opts.Store != nil {
				opts.Store.Put(hash, s.text)
			}

			b := tok.CountText(s.text)
			a := tok.CountText(compressed)
			reps = append(reps, replacement{
				start: s.start, end: s.end, repl: encodeJSONString(compressed)})
			outcomes = append(outcomes, BlockOutcome{
				MessageIndex: msgIdx,
				Index:        s.blockIndex,
				BlockType:    s.blockType,
				Action:       "replayed",
				TokensBefore: b,
				TokensAfter:  a,
				CacheKey:     hash,
			})
			before += b
			after += a
		}
	}
	return reps, outcomes, before, after
}
