package livezone

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// TestBugProof demonstrates the fix: before this change, a tool_result with
// structured array content gave blocks=0, reason=no_candidates. After, it
// returns blocks>0 with the nested elements visible.
func TestBugProof(t *testing.T) {
	// A real-shaped tool_result with ('image','text') content
	log := compressibleLog()
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := bodyWithToolResultArray(log, b64)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := liveOptions(t)
	opts.Store = store

	res := Dispatch(body, opts)

	// BEFORE: res.Blocks would be empty (blocks=0)
	// AFTER: res.Blocks contains the nested elements
	if len(res.Blocks) == 0 {
		t.Fatal("BUG UNFIXED: blocks=0, nested content is still invisible")
	}

	// Find the image block and the text block
	var imageBlock, textBlock *BlockOutcome
	for i := range res.Blocks {
		if res.Blocks[i].BlockType == "image" {
			imageBlock = &res.Blocks[i]
		}
		if res.Blocks[i].BlockType == "text" {
			textBlock = &res.Blocks[i]
		}
	}

	if imageBlock == nil {
		t.Error("image block not found in outcomes (nested content still invisible)")
	}
	if textBlock == nil {
		t.Error("text block not found in outcomes (nested content still invisible)")
	}

	if imageBlock != nil && imageBlock.ContentIndex < 0 {
		t.Error("image block has no ContentIndex (nested tracking is broken)")
	}
	if textBlock != nil && textBlock.ContentIndex < 0 {
		t.Error("text block has no ContentIndex (nested tracking is broken)")
	}

	t.Logf("BUG FIXED: blocks=%d, image and text blocks are now visible with ContentIndex", len(res.Blocks))
}
