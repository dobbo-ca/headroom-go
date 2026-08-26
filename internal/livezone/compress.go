package livezone

import (
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// blockResult is one block's compression outcome.
type blockResult struct {
	accepted     bool
	action       string
	strategy     string
	replacement  string
	tokensBefore int
	tokensAfter  int
	cacheKey     string
}

// acceptsI5 is invariant I5: a replacement is kept only when it strictly
// shrinks the token count. Equal counts are rejected — the body would not
// get smaller and the CCR marker would be pure overhead.
func acceptsI5(tokensBefore, tokensAfter int) bool { return tokensAfter < tokensBefore }

// injectCCRMarker appends a <<ccr:HASH>> retrieval marker to the compressed
// text, keyed on the ORIGINAL. The marker goes at the very end, on its own
// line, so the block's own content is never split or reordered (I6).
//
// This does NOT write to the store. A nil store disables marker injection
// entirely.
func injectCCRMarker(original, compressed string, store ccr.Store) (string, string) {
	if store == nil {
		return compressed, ""
	}
	hash := ccr.ComputeKey([]byte(original))
	marker := ccr.MarkerFor(hash)
	if strings.HasSuffix(compressed, "\n") {
		return compressed + marker, hash
	}
	return compressed + "\n" + marker, hash
}

// compressBlock runs one block's text through the router, appends the CCR
// marker, and applies the I5 gate.
//
// The marker is injected BEFORE the gate on purpose: it costs tokens, so the
// decision about whether compression was worthwhile must account for it.
func compressBlock(text string, ctx transform.CompressionContext, opts Options, tok tokenizer.Tokenizer) blockResult {
	if opts.Router == nil {
		return blockResult{action: "no_op"}
	}
	if opts.Store == nil {
		// The router's offload transforms (json_offload, diff_noise, the
		// log/diff/search compressors) stash the original under opts.Store
		// unconditionally — they have no way to produce a resolvable
		// marker without one. Bail before Compress rather than let a nil
		// Store reach a store.Put and panic.
		return blockResult{action: "no_op"}
	}

	// The router's compressors and offload transforms Put the original
	// unconditionally, inside Compress, long before the I5 gate can run.
	// Buffer those writes so a rejected block leaves no orphan entry: the
	// staged originals reach the real store only once the gate accepts.
	staged := &stagingStore{backing: opts.Store}
	res := opts.Router.Compress(text, ctx, staged)
	if res.Output == "" || res.Output == text {
		return blockResult{action: "no_op"}
	}

	strategy := strings.Join(res.StepsApplied, "+")
	candidate, hash := injectCCRMarker(text, res.Output, opts.Store)

	before := tok.CountText(text)
	after := tok.CountText(candidate)
	if !acceptsI5(before, after) {
		return blockResult{
			action:       "rejected_tokens",
			strategy:     strategy,
			tokensBefore: before,
			tokensAfter:  after,
		}
	}
	// Accepted: the compressor's own inline marker survives into the body
	// sent upstream, so the hash it names must resolve.
	staged.commit()

	return blockResult{
		accepted:     true,
		action:       "compressed",
		strategy:     strategy,
		replacement:  candidate,
		tokensBefore: before,
		tokensAfter:  after,
		cacheKey:     hash,
	}
}

// stagingStore buffers a router's CCR writes until the I5 gate decides.
//
// Every compressor and offload transform stashes the original inside
// Compress, because it has no other way to emit a resolvable marker. The gate
// runs afterwards, so writing straight through would leave the original of a
// REJECTED block in the store forever: an entry no marker on the wire ever
// names.
//
// Reads fall through to the backing store so a transform that stores and then
// re-reads a hash within one Compress call still sees its own write.
type stagingStore struct {
	backing ccr.Store
	hashes  []string
	staged  map[string]string
}

func (s *stagingStore) Put(hash, payload string) {
	if s.staged == nil {
		s.staged = make(map[string]string, 1)
	}
	if _, dup := s.staged[hash]; !dup {
		// Insertion order is replayed on commit, so the backing store sees a
		// deterministic sequence (I4).
		s.hashes = append(s.hashes, hash)
	}
	s.staged[hash] = payload
}

func (s *stagingStore) Get(hash string) (string, bool) {
	if v, ok := s.staged[hash]; ok {
		return v, true
	}
	return s.backing.Get(hash)
}

func (s *stagingStore) Len() int { return len(s.staged) + s.backing.Len() }

// commit replays the staged writes into the backing store, in Put order.
func (s *stagingStore) commit() {
	for _, h := range s.hashes {
		s.backing.Put(h, s.staged[h])
	}
}
