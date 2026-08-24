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
// This does NOT write to the store: the caller stores the original only
// after the I5 gate accepts, so a rejected block leaves no orphan entry.
// A nil store disables marker injection entirely.
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
func compressBlock(text string, opts Options, tok tokenizer.Tokenizer) blockResult {
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

	res := opts.Router.Compress(text, transform.CompressionContext{Query: opts.Query}, opts.Store)
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
