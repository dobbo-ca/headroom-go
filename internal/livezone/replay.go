package livezone

import (
	"strings"

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
			//
			// If the store will not hand it back, DROP the replay and let
			// the original through. That costs a prompt-cache miss on this
			// block, which is real; putting a marker the model cannot
			// dereference into the frozen prefix costs the model sight of
			// its own work for the rest of the session, which is worse.
			//
			// Forget the entry as well. The block sits below the frozen
			// floor, so the fresh pass can never reach it again — the entry
			// can only fail this same way on every later turn, costing a
			// store read each time. The staleness sweep would not collect
			// it either, because replay looks it up every turn and that is
			// exactly what marks an entry live.
			if opts.Store != nil {
				opts.Store.Put(hash, s.text)
				// The canonical entry is not the only one the replayed
				// text names. The heuristic compressors append their own
				// inline "hash=" marker keyed with ComputeKeyMD5 over the
				// same input, and that entry expires on the same TTL. Only
				// the canonical one is rebuildable from `hash`, so refresh
				// the MD5 one too — but only when the text actually names
				// it, or this would write an orphan no marker points at.
				//
				// An intermediate hash from a multi-step chain cannot be
				// rebuilt from the body at all. storeResolves below then
				// declines the replay, which is the right answer: a
				// dangling marker is worse than a cache miss.
				if md5 := ccr.ComputeKeyMD5([]byte(s.text)); strings.Contains(compressed, md5) {
					opts.Store.Put(md5, s.text)
				}
				if !storeResolves(opts.Store, hash, s.text, compressed) {
					opts.Replay.Forget(hash)
					outcomes = append(outcomes, BlockOutcome{
						MessageIndex: msgIdx, Index: s.blockIndex,
						BlockType: s.blockType, Action: "store_unresolvable", CacheKey: hash})
					continue
				}
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
