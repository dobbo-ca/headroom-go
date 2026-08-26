package livezone

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/tidwall/gjson"
)

// bodyWithToolResultArray builds a tool_result with structured array content.
func bodyWithToolResultArray(textContent, b64Data string) []byte {
	textQuoted, _ := json.Marshal(textContent)
	b64Quoted, _ := json.Marshal(b64Data)
	var b strings.Builder
	b.WriteString(`{"model":"claude-3-5-sonnet-20241022","messages":[`)
	b.WriteString(`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"bash","input":{"command":"echo test"}}]},`)
	b.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":[`)
	b.WriteString(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":`)
	b.Write(b64Quoted)
	b.WriteString(`}},{"type":"text","text":`)
	b.Write(textQuoted)
	b.WriteString(`}]}]}]}`)
	return []byte(b.String())
}

// bodyWithToolResultArrayImage builds a tool_result with a single image in array content.
func bodyWithToolResultArrayImage(b64Data, mediaType string) []byte {
	b64Quoted, _ := json.Marshal(b64Data)
	mtQuoted, _ := json.Marshal(mediaType)
	var b strings.Builder
	b.WriteString(`{"model":"claude-3-5-sonnet-20241022","messages":[`)
	b.WriteString(`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"bash","input":{"command":"echo test"}}]},`)
	b.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":[`)
	b.WriteString(`{"type":"image","source":{"type":"base64","media_type":`)
	b.Write(mtQuoted)
	b.WriteString(`,"data":`)
	b.Write(b64Quoted)
	b.WriteString(`}}]}]}]}`)
	return []byte(b.String())
}

// TestImageToolResultIsReachableWithTransformOff is Piece 1's real deliverable:
// the dispatcher now SEES image blocks, even though it does not transform them.
// Before: blocks=0, reason=no_candidates. After: len(Blocks)==1, Action=="image_declined",
// body byte-identical.
func TestImageToolResultIsReachableWithTransformOff(t *testing.T) {
	b64 := strings.Repeat("iVBORw0KGgo", 100) // ~1100 chars
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}]}`)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := Options{Store: store, FrozenCount: -1}

	res := Dispatch(body, opts)

	// The bug was: blocks=0, reason=no_candidates. Now it must see the block.
	if len(res.Blocks) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1 (the image block must be seen)", len(res.Blocks))
	}
	blk := res.Blocks[0]
	if blk.BlockType != "image" {
		t.Errorf("BlockType = %q, want %q", blk.BlockType, "image")
	}
	if blk.Action != "image_declined" {
		t.Errorf("Action = %q, want %q (transform is not wired yet)", blk.Action, "image_declined")
	}
	if blk.ContentIndex != 0 {
		t.Errorf("ContentIndex = %d, want 0 (nested at element 0)", blk.ContentIndex)
	}

	// Body must be byte-identical
	if string(res.Body) != string(body) {
		t.Error("body changed when the transform is off")
	}
	if res.Applied {
		t.Error("Applied must be false when no transform is wired")
	}
	if res.Reason != ReasonNoCandidates {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonNoCandidates)
	}
}

// TestNestedTextBlockActuallyCompresses proves the full nested path end to end
// on a real log block: Applied, Action=="compressed", Strategy non-empty,
// TokensAfter < TokensBefore, output valid JSON, image source.data unchanged.
func TestNestedTextBlockActuallyCompresses(t *testing.T) {
	log := compressibleLog() // ~11 KB
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := bodyWithToolResultArray(log, b64)

	res := Dispatch(body, liveOptions(t))

	if !res.Applied {
		t.Fatalf("Applied = false, want true; reason = %q", res.Reason)
	}
	if len(res.Blocks) == 0 {
		t.Fatal("no blocks recorded")
	}

	// Find the nested text block
	var textBlock *BlockOutcome
	for i := range res.Blocks {
		if res.Blocks[i].BlockType == "text" {
			textBlock = &res.Blocks[i]
			break
		}
	}
	if textBlock == nil {
		t.Fatal("nested text block not found in outcomes")
	}

	if textBlock.Action != "compressed" {
		t.Errorf("Action = %q, want %q", textBlock.Action, "compressed")
	}
	if textBlock.Strategy == "" {
		t.Error("Strategy is empty, want non-empty (the compressor name)")
	}
	if textBlock.TokensAfter >= textBlock.TokensBefore {
		t.Errorf("TokensAfter %d >= TokensBefore %d, must shrink", textBlock.TokensAfter, textBlock.TokensBefore)
	}
	if textBlock.ContentIndex < 0 {
		t.Errorf("ContentIndex = %d, must be >= 0 for a nested block", textBlock.ContentIndex)
	}

	// Output must be valid JSON
	var v map[string]any
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Image source.data must be byte-identical
	outData := gjson.GetBytes(res.Body, "messages.1.content.0.content.0.source.data").String()
	if outData != b64 {
		t.Error("image source.data changed when only the text block should have been rewritten")
	}
}

// TestNestedCompressedBlockStoresBothMarkerSurfaces confirms store.Len() == 2
// after a nested compression (canonical BLAKE3 + compressor MD5), and every hash
// in the output body resolves.
func TestNestedCompressedBlockStoresBothMarkerSurfaces(t *testing.T) {
	log := compressibleLog()
	b64 := strings.Repeat("iVBORw0KGgo", 50)
	body := bodyWithToolResultArray(log, b64)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := liveOptions(t)
	opts.Store = store

	lenBefore := store.Len()
	res := Dispatch(body, opts)

	if !res.Applied {
		t.Fatalf("Applied = false, reason = %q", res.Reason)
	}

	lenAfter := store.Len()
	added := lenAfter - lenBefore
	if added != 2 {
		t.Errorf("store grew by %d entries, want 2 (canonical + MD5)", added)
	}

	// Every hash in the output must resolve
	for _, h := range ccr.HashesIn(string(res.Body)) {
		if _, ok := store.Get(h); !ok {
			t.Errorf("hash %q in the output body does not resolve in the store", h)
		}
	}
}

// TestImageSlotWritesNothingToTheStore confirms store.Len() is unchanged across
// a Dispatch with an image block, whether the transform is on or off (Piece 2
// will be off for now).
func TestImageSlotWritesNothingToTheStore(t *testing.T) {
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}]}`)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := Options{Store: store, FrozenCount: -1}

	lenBefore := store.Len()
	Dispatch(body, opts)
	lenAfter := store.Len()

	if lenAfter != lenBefore {
		t.Errorf("store.Len() changed from %d to %d, want unchanged (images write nothing to the store)", lenBefore, lenAfter)
	}
}

// TestUnreachableOutcomeDistinguishesFromNoCandidates asserts a ('document',)
// array gives Reason==ReasonNoCandidates AND len(Blocks)==1 with
// Action=="unreachable", while an empty content array gives Reason==ReasonNoCandidates
// AND len(Blocks)==0.
func TestUnreachableOutcomeDistinguishesFromNoCandidates(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantBlocks int
		wantAction string
	}{
		{
			"unreachable nested type",
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"document","data":"xyz"}]}]}]}`,
			1,
			"unreachable",
		},
		{
			"empty content array",
			`{"messages":[{"role":"user","content":[]}]}`,
			0,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Dispatch([]byte(tt.body), Options{FrozenCount: -1})

			if res.Reason != ReasonNoCandidates {
				t.Errorf("Reason = %q, want %q", res.Reason, ReasonNoCandidates)
			}
			if len(res.Blocks) != tt.wantBlocks {
				t.Errorf("len(Blocks) = %d, want %d", len(res.Blocks), tt.wantBlocks)
			}
			if tt.wantBlocks > 0 && res.Blocks[0].Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", res.Blocks[0].Action, tt.wantAction)
			}
		})
	}
}

// TestTwoNestedBlocksSpliceWithoutOverlap asserts ('image','text') shape with
// both accepted: two Rewritten ranges, non-overlapping, ascending, output valid JSON.
func TestTwoNestedBlocksSpliceWithoutOverlap(t *testing.T) {
	log := compressibleLog()
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := bodyWithToolResultArray(log, b64)

	res := Dispatch(body, liveOptions(t))

	if !res.Applied {
		t.Fatalf("Applied = false, reason = %q", res.Reason)
	}
	if len(res.Rewritten) == 0 {
		t.Fatal("no ranges rewritten")
	}

	// Ranges must be non-overlapping and ascending
	for i := 1; i < len(res.Rewritten); i++ {
		prev := res.Rewritten[i-1]
		curr := res.Rewritten[i]
		if curr.Start < prev.End {
			t.Errorf("range %d [%d,%d) overlaps with range %d [%d,%d)", i, curr.Start, curr.End, i-1, prev.Start, prev.End)
		}
	}

	// Output must be valid JSON
	var v map[string]any
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
}

// TestDispatchIsDeterministicWithNestedBlocks extends determinism_test.go:
// 25 runs, byte-equal Body, equal Rewritten, equal Blocks including ContentIndex.
func TestDispatchIsDeterministicWithNestedBlocks(t *testing.T) {
	log := compressibleLog()
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := bodyWithToolResultArray(log, b64)

	runs := 25
	var prev Result
	for i := 0; i < runs; i++ {
		res := Dispatch(body, liveOptions(t))
		if i == 0 {
			prev = res
			continue
		}

		if string(res.Body) != string(prev.Body) {
			t.Fatalf("run %d: Body differs from run 0", i)
		}
		if len(res.Rewritten) != len(prev.Rewritten) {
			t.Fatalf("run %d: len(Rewritten) = %d, want %d", i, len(res.Rewritten), len(prev.Rewritten))
		}
		for j := range res.Rewritten {
			if res.Rewritten[j] != prev.Rewritten[j] {
				t.Errorf("run %d: Rewritten[%d] = %+v, want %+v", i, j, res.Rewritten[j], prev.Rewritten[j])
			}
		}
		if len(res.Blocks) != len(prev.Blocks) {
			t.Fatalf("run %d: len(Blocks) = %d, want %d", i, len(res.Blocks), len(prev.Blocks))
		}
		for j := range res.Blocks {
			if res.Blocks[j].ContentIndex != prev.Blocks[j].ContentIndex {
				t.Errorf("run %d: Blocks[%d].ContentIndex = %d, want %d", i, j, res.Blocks[j].ContentIndex, prev.Blocks[j].ContentIndex)
			}
		}
	}
}

// TestDispatchImageFitOffLeavesImageBytesUntouched proves ImageFit=false
// keeps images byte-identical.
func TestDispatchImageFitOffLeavesImageBytesUntouched(t *testing.T) {
	b64 := genPNG(2558, 1370)
	body := bodyWithToolResultArrayImage(b64, "image/png")

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := Options{Store: store, FrozenCount: -1, ImageFit: false}

	res := Dispatch(body, opts)

	// Must see the image block
	if len(res.Blocks) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1", len(res.Blocks))
	}
	if res.Blocks[0].Action != "image_declined" {
		t.Errorf("Action = %q, want %q", res.Blocks[0].Action, "image_declined")
	}
	// Body must be byte-identical
	if string(res.Body) != string(body) {
		t.Error("body changed when ImageFit=false")
	}
}

// TestDispatchImageFitOnResizesAndReportsTokens proves ImageFit=true
// transforms the image and reports visual tokens.
func TestDispatchImageFitOnResizesAndReportsTokens(t *testing.T) {
	b64 := genPNG(2558, 1370)
	body := bodyWithToolResultArrayImage(b64, "image/png")

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	opts := Options{Store: store, FrozenCount: -1, ImageFit: true}

	res := Dispatch(body, opts)

	// Must see the image block
	if len(res.Blocks) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1", len(res.Blocks))
	}
	blk := res.Blocks[0]
	if blk.Action != "image_resized" {
		t.Errorf("Action = %q, want %q", blk.Action, "image_resized")
	}
	// Token counts must be non-zero and decreasing
	if blk.TokensBefore == 0 || blk.TokensAfter == 0 {
		t.Errorf("tokens before/after = %d/%d, want non-zero", blk.TokensBefore, blk.TokensAfter)
	}
	if blk.TokensAfter >= blk.TokensBefore {
		t.Errorf("tokens %d→%d, want after < before", blk.TokensBefore, blk.TokensAfter)
	}
	// Expected for 2558x1370: 4508 → 1566
	if blk.TokensBefore != 4508 || blk.TokensAfter != 1566 {
		t.Errorf("tokens = %d→%d, want 4508→1566", blk.TokensBefore, blk.TokensAfter)
	}
	// Body must be valid JSON
	if !gjson.ValidBytes(res.Body) {
		t.Error("output is not valid JSON")
	}
	// media_type must be unchanged
	mt := gjson.GetBytes(res.Body, "messages.1.content.0.content.0.source.media_type").String()
	if mt != "image/png" {
		t.Errorf("media_type = %q, want %q", mt, "image/png")
	}
	// data field must be different (resized)
	newData := gjson.GetBytes(res.Body, "messages.1.content.0.content.0.source.data").String()
	if newData == b64 {
		t.Error("image data unchanged when ImageFit=true")
	}
	// The new data must decode to the target size
	if newData != "" {
		tw, th := resizedSize(2558, 1370, 1568, 1568)
		cfg, _, err := image.DecodeConfig(strings.NewReader(mustDecodeB64(newData)))
		if err != nil {
			t.Fatalf("decode resized image: %v", err)
		}
		if cfg.Width != tw || cfg.Height != th {
			t.Errorf("resized dimensions = %dx%d, want %dx%d", cfg.Width, cfg.Height, tw, th)
		}
	}
}

func mustDecodeB64(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
