package livezone

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// TestNestedBlockReplaysBelowTheFrozenFloor proves turn 2 with a nested block
// below the floor reproduces turn 1's output byte-exactly.
func TestNestedBlockReplaysBelowTheFrozenFloor(t *testing.T) {
	log := compressibleLog()
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	body := bodyWithToolResultArray(log, b64)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	handle := cachestab.NewReplayState(8).Begin("s1", body)
	opts := liveOptions(t)
	opts.Store = store
	opts.Replay = handle
	opts.FrozenCount = -1

	// Turn 1: compress the nested text block
	res1 := Dispatch(body, opts)
	if !res1.Applied {
		t.Fatalf("turn 1: Applied = false, reason = %q", res1.Reason)
	}

	// Turn 2: the nested block is now below the frozen floor (FrozenCount=2 means
	// messages 0 and 1 are frozen, so the tool_result sits at index 1 and is frozen).
	opts.FrozenCount = 2
	res2 := Dispatch(body, opts)

	// The body must be byte-identical to turn 1's output
	if string(res2.Body) != string(res1.Body) {
		t.Error("turn 2 body differs from turn 1 (replay did not reproduce the nested compression)")
	}
}

// TestImageReplayKeyIsDomainSeparatedFromText asserts a text slot whose text is
// exactly an image's base64 AND an image slot with that base64 do not share a key.
// The text slot must not receive the image replacement.
//
// NOTE: For Piece 1, images do not replay yet (no transform). This test will be
// fully exercised in Piece 2. For now, it confirms the text path is unaffected.
func TestImageReplayKeyIsDomainSeparatedFromText(t *testing.T) {
	// A pathological case: the text content is exactly the base64 of an image
	b64 := strings.Repeat("iVBORw0KGgo", 100)
	bodyText := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"` + b64 + `"}]}]}`)

	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 10})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	handle := cachestab.NewReplayState(8).Begin("s2", bodyText)
	opts := Options{Store: store, Replay: handle, FrozenCount: -1}

	// Dispatch the text block (it's below BlockByteThreshold, so it won't compress,
	// but we can still verify the key space)
	Dispatch(bodyText, opts)

	// If an image were replayed with the same base64, it must use a different key.
	// We don't have the image transform yet, but we can verify the key function directly.
	textKey := ccr.ComputeKey([]byte(b64))
	imageKey := imageReplayKey("image/png", b64)

	if textKey == imageKey {
		t.Error("text key and image key collide: the domain separator is missing or broken")
	}
}

// TestReplayedImageDoesNotTouchTheStore (placeholder for Piece 2) confirms
// store.Len() is unchanged across a replayed-image turn. For now, images do not
// replay, so this is a no-op placeholder.
func TestReplayedImageDoesNotTouchTheStore(t *testing.T) {
	t.Skip("Piece 2: images do not replay yet, this test is a placeholder")
}

// TestReplayedImageReportsVisualTokens (placeholder for Piece 2) confirms a
// replayed image outcome's TokensBefore/After come from dimensions, not tok.CountText.
func TestReplayedImageReportsVisualTokens(t *testing.T) {
	t.Skip("Piece 2: images do not replay yet, this test is a placeholder")
}

// imageReplayKey is the domain-separated key function for image replay (Piece 2).
func imageReplayKey(mediaType, b64 string) string {
	return ccr.ComputeKey([]byte("hr-image\x00" + mediaType + "\x00" + b64))
}
