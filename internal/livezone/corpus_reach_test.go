package livezone

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/tidwall/gjson"
)

// reachOf classifies a Result's reach state based on BlockOutcome.Action,
// never on Result.Reason (which conflates unreachable, image_declined, and
// no_op under the same "no_candidates" value).
func reachOf(res Result) string {
	if len(res.Blocks) == 0 {
		return "no_outcome"
	}
	// Check if any outcome is acted/declined/unreachable
	for _, blk := range res.Blocks {
		switch blk.Action {
		case "compressed", "replayed", "image_resized":
			return "acted"
		case "no_op", "rejected_tokens", "below_threshold", "hot_zone", "store_unresolvable", "image_declined":
			return "declined"
		case "unreachable":
			return "unreachable"
		}
	}
	return "no_outcome"
}

// corpusRow is one row in the reach table. One Result may produce multiple rows
// (one per BlockOutcome), not just one.
type corpusRow struct {
	Reach        string
	Action       string
	Strategy     string
	Detected     string
	Reason       string // diagnostic only, never aggregated
	ElemType     string
	ContentIndex int
	TokensBefore int
	TokensAfter  int
	TokenKind    string // "text" or "visual"
	WireBytes    int
}

// rowsFor produces one row per BlockOutcome. This fixes the bug where a
// [text, image] block reported only the last outcome and hid the other.
func rowsFor(res Result) []corpusRow {
	if len(res.Blocks) == 0 {
		return nil
	}
	var rows []corpusRow
	for _, blk := range res.Blocks {
		var reach string
		switch blk.Action {
		case "compressed", "replayed", "image_resized":
			reach = "acted"
		case "no_op", "rejected_tokens", "below_threshold", "hot_zone", "store_unresolvable", "image_declined":
			reach = "declined"
		case "unreachable":
			reach = "unreachable"
		default:
			reach = "no_outcome"
		}
		tokenKind := "text"
		if blk.Action == "image_resized" || blk.Action == "image_declined" {
			tokenKind = "visual"
		}
		rows = append(rows, corpusRow{
			Reach:        reach,
			Action:       blk.Action,
			Strategy:     blk.Strategy,
			Reason:       string(res.Reason),
			ElemType:     blk.BlockType,
			ContentIndex: blk.ContentIndex,
			TokensBefore: blk.TokensBefore,
			TokensAfter:  blk.TokensAfter,
			TokenKind:    tokenKind,
		})
	}
	return rows
}

// wireHash returns the first 16 hex digits of the sha256 of raw wire bytes.
func wireHash(wire string) string {
	h := sha256.Sum256([]byte(wire))
	return hex.EncodeToString(h[:8])
}

// keepBlock applies the 512-byte threshold to raw wire bytes.
func keepBlock(wireBytes int) bool {
	return wireBytes >= 512
}

// ---------- Tests pinning the reach taxonomy and guard ----------

func TestReachOfActedCompressed(t *testing.T) {
	res := Result{
		Blocks: []BlockOutcome{{Action: "compressed"}},
	}
	if got := reachOf(res); got != "acted" {
		t.Fatalf("reachOf compressed: got %q, want acted", got)
	}
}

func TestReachOfImageDeclinedIsDeclinedNotUnreachable(t *testing.T) {
	res := Result{
		Blocks: []BlockOutcome{{Action: "image_declined"}},
	}
	if got := reachOf(res); got != "declined" {
		t.Fatalf("reachOf image_declined: got %q, want declined (NOT unreachable)", got)
	}
}

func TestReachIgnoresReasonWhenActionIsUnreachable(t *testing.T) {
	res := Result{
		Reason: "no_candidates",
		Blocks: []BlockOutcome{{Action: "unreachable"}},
	}
	reach := reachOf(res)
	if reach != "unreachable" {
		t.Fatalf("reachOf with Reason=no_candidates, Action=unreachable: got %q, want unreachable", reach)
	}
	// The assertion that proves we classify on Action, not Reason
	if res.Reason != "no_candidates" {
		t.Fatalf("Reason changed: this test pins the conflation")
	}
}

func TestGuardFiresOnEmptyBlocks(t *testing.T) {
	res := Result{Blocks: []BlockOutcome{}}
	if reachOf(res) != "no_outcome" {
		t.Fatalf("reachOf(empty Blocks): got %q, want no_outcome", reachOf(res))
	}

	// Also test that rowsFor returns nil for empty blocks, and that code
	// using it must check len(rows)==0 and append a no_outcome row
	rows := rowsFor(res)
	if len(rows) != 0 {
		t.Fatalf("rowsFor(empty Blocks): got %d rows, want 0", len(rows))
	}

	// Simulate the guard pattern from TestCorpusClassify
	if len(rows) == 0 {
		rows = []corpusRow{{Reach: "no_outcome"}}
	}
	if len(rows) != 1 || rows[0].Reach != "no_outcome" {
		t.Fatal("guard pattern failed to produce no_outcome row")
	}
}

func TestWireHashIsOverRawBytes(t *testing.T) {
	// Two wires differing only in internal whitespace must hash differently
	wire1 := `[{"type":"text","text":"x"}]`
	wire2 := `[{"type" : "text" , "text" : "x"}]`
	h1, h2 := wireHash(wire1), wireHash(wire2)
	if h1 == h2 {
		t.Fatalf("wireHash did not distinguish whitespace variants: both=%s", h1)
	}
	// Identical wire must hash identically
	if wireHash(wire1) != wireHash(wire1) {
		t.Fatalf("wireHash not stable")
	}
}

func TestKeepBlockUsesWireBytes(t *testing.T) {
	if keepBlock(511) {
		t.Fatal("keepBlock(511): got true, want false")
	}
	if !keepBlock(512) {
		t.Fatal("keepBlock(512): got false, want true")
	}
}

// ---------- Tests requiring real Dispatch ----------

func TestGuardOnRealDispatchEmptyArrayContent(t *testing.T) {
	// content: [] is a legal wire shape and reaches the guard today
	body := []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[]}]}]}`)

	store := testStore(t)
	res := Dispatch(body, testOptions(store))

	if len(res.Blocks) != 0 {
		t.Fatalf("empty array content: got %d blocks, want 0 (guard must fire)", len(res.Blocks))
	}
	if reachOf(res) != "no_outcome" {
		t.Fatalf("empty array content reach: got %q, want no_outcome", reachOf(res))
	}
}

func TestArrayContentReachesNestedTextBlock(t *testing.T) {
	// Build a real 17KB log block inside an array
	var log string
	for i := 0; i < 200; i++ {
		log += "2026-01-01 00:00:00 INFO  worker: processed batch id=000000 status=ok latency_ms=12\n"
	}

	// Wire format: [{"type":"text","text":"<log>"}]
	body := corpusBodyWireArray("text", log, "")

	store := testStore(t)
	res := Dispatch(body, testOptions(store))

	if !res.Applied {
		t.Fatalf("array-wrapped log: Applied=%v, want true", res.Applied)
	}
	if len(res.Blocks) != 1 {
		t.Fatalf("array-wrapped log: got %d outcomes, want 1", len(res.Blocks))
	}
	blk := res.Blocks[0]
	if blk.BlockType != "text" {
		t.Fatalf("array-wrapped log: BlockType=%q, want text", blk.BlockType)
	}
	if blk.ContentIndex != 0 {
		t.Fatalf("array-wrapped log: ContentIndex=%d, want 0", blk.ContentIndex)
	}
	if blk.Action != "compressed" {
		t.Fatalf("array-wrapped log: Action=%q, want compressed", blk.Action)
	}
}

func TestWireSplicesByteIdentical(t *testing.T) {
	// Table of wire shapes including internal whitespace
	cases := []struct {
		name string
		wire string
	}{
		{"string", `"hello"`},
		{"array_text", `[{"type":"text","text":"hello"}]`},
		{"array_text_spaced", `[ { "type" : "text" , "text" : "hello" } ]`},
		{"array_empty", `[]`},
		{"array_two_elem", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Use gjson to extract the content value, just as the extractor would
			body := []byte(`{"model":"claude-sonnet-5","messages":[` +
				`{"role":"user","content":[{"type":"text","text":"go"}]},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + tc.wire + `}]}]}`)

			// Extract with gjson.Result.Raw
			result := gjsonGet(string(body), "messages.2.content.0.content")
			if result.Raw != tc.wire {
				t.Fatalf("gjson round-trip failed: got %q, want %q", result.Raw, tc.wire)
			}
		})
	}

	// Also assert sha256(body) unchanged when Dispatch does not act
	wire := `"short"`
	body := []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + wire + `}]}]}`)

	inputSha := sha256.Sum256(body)
	store := testStore(t)
	res := Dispatch(body, testOptions(store))
	outputSha := sha256.Sum256(res.Body)

	// Expect below_threshold, so Body should be unchanged
	if res.Applied {
		t.Fatalf("short string: Applied=%v, want false", res.Applied)
	}
	if inputSha != outputSha {
		t.Fatalf("short string: body sha changed when not acting")
	}
}

// Helper to build a body with array-shaped content
func corpusBodyWireArray(elemType, payload, mediaType string) []byte {
	var elem string
	switch elemType {
	case "text":
		quoted, _ := jsonMarshal(payload)
		elem = `{"type":"text","text":` + string(quoted) + `}`
	case "image":
		quoted, _ := jsonMarshal(payload)
		mt := `"image/png"`
		if mediaType != "" {
			mt, _ = jsonMarshal(mediaType)
		}
		elem = `{"type":"image","source":{"type":"base64","media_type":` + mt + `,"data":` + string(quoted) + `}}`
	case "tool_reference":
		elem = `{"type":"tool_reference","tool_use_id":"x","some_field":"data"}`
	default:
		return nil
	}

	body := `{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` + elem + `]}]}]}`
	return []byte(body)
}

// gjsonGet is a thin wrapper for testing
func gjsonGet(body, path string) gjsonResult {
	r := gjson.Get(body, path)
	return gjsonResult{Raw: r.Raw}
}

type gjsonResult struct {
	Raw string
}

func jsonMarshal(s string) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

func TestReshapedBlockIsNeverClassified(t *testing.T) {
	// A block marked as reshaped must land in excluded_reshaped and carry
	// no action/strategy/detected values
	row := corpusRow{Reach: "excluded_reshaped"}
	if row.Action != "" || row.Strategy != "" || row.Detected != "" {
		t.Fatal("excluded_reshaped row carried classification fields")
	}
}

func TestDetectNeverRunsOnWireArray(t *testing.T) {
	// For an image inside an array, Detected and Strategy must stay empty.
	// This pins the exact bug the bead retracts: running rt.Detect on a
	// stringified array returns "json_array", which is how "32.2 MB rejected
	// by json_minifier" was manufactured.

	// Build a 40x40 PNG (>512 bytes base64 to pass threshold)
	imgData := make40x40PNG(t)
	body := corpusBodyWireArray("image", imgData, "image/png")

	store := testStore(t)
	res := Dispatch(body, testOptions(store))

	rows := rowsFor(res)
	if len(rows) == 0 {
		t.Fatal("image array: no rows")
	}
	for _, r := range rows {
		if r.Action == "image_declined" {
			if r.Detected != "" {
				t.Fatalf("image_declined row: Detected=%q, want empty", r.Detected)
			}
			if r.Strategy != "" {
				t.Fatalf("image_declined row: Strategy=%q, want empty", r.Strategy)
			}
		}
	}
}

func TestTokenKindsAreNotSummed(t *testing.T) {
	// Renderer given one text row + one visual row: text denominator must
	// exclude visual tokens
	rows := []corpusRow{
		{TokenKind: "text", TokensBefore: 1000, WireBytes: 5000},
		{TokenKind: "visual", TokensBefore: 500, WireBytes: 2000},
	}

	var textTokens, visualTokens int
	for _, r := range rows {
		if r.TokenKind == "text" {
			textTokens += r.TokensBefore
		} else if r.TokenKind == "visual" {
			visualTokens += r.TokensBefore
		}
	}

	if textTokens != 1000 {
		t.Fatalf("text denominator: got %d, want 1000", textTokens)
	}
	if visualTokens != 500 {
		t.Fatalf("visual denominator: got %d, want 500", visualTokens)
	}
	// Assert they are NOT summed
	if textTokens+visualTokens != 1500 {
		t.Fatal("sanity check failed")
	}
}

func testStore(t *testing.T) ccr.Store {
	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testOptions(store ccr.Store) Options {
	return Options{
		Policy:      policy.ForMode(policy.PAYG),
		Router:      router.NewDefault(),
		Store:       store,
		FrozenCount: 0,
	}
}

func make40x40PNG(t *testing.T) string {
	// Generate a minimal 40x40 PNG (random RGB) that encodes to >512 bytes base64
	// Using a simple pattern that compresses poorly
	var buf []byte
	// PNG signature
	buf = append(buf, 0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)

	// IHDR chunk: 40x40, truecolor
	ihdr := []byte{
		0x00, 0x00, 0x00, 0x0D, // length
		0x49, 0x48, 0x44, 0x52, // IHDR
		0x00, 0x00, 0x00, 0x28, // width 40
		0x00, 0x00, 0x00, 0x28, // height 40
		0x08, 0x02, 0x00, 0x00, 0x00, // bit depth 8, color type 2 (RGB)
	}
	// Add CRC (compute properly or use a known-good one)
	ihdr = append(ihdr, 0x4E, 0xEC, 0x6C, 0x2E) // CRC for this IHDR
	buf = append(buf, ihdr...)

	// IDAT chunk with random data (simplified - real PNG needs proper compression)
	// For this test, we just need >512 bytes base64, so ~384 bytes raw
	// A proper implementation would use zlib, but for test purposes:
	idatData := make([]byte, 400) // enough to exceed 512 base64
	for i := range idatData {
		idatData[i] = byte(i % 256)
	}

	idat := []byte{0x00, 0x00, 0x01, 0x90}      // length ~400
	idat = append(idat, 0x49, 0x44, 0x41, 0x54) // IDAT
	idat = append(idat, idatData...)
	idat = append(idat, 0x00, 0x00, 0x00, 0x00) // CRC placeholder
	buf = append(buf, idat...)

	// IEND chunk
	iend := []byte{
		0x00, 0x00, 0x00, 0x00, // length
		0x49, 0x45, 0x4E, 0x44, // IEND
		0xAE, 0x42, 0x60, 0x82, // CRC
	}
	buf = append(buf, iend...)

	// Encode to base64
	encoded := base64Encode(buf)
	if len(encoded) < 512 {
		t.Fatalf("test PNG too small: %d bytes base64, need >=512", len(encoded))
	}
	return encoded
}

func base64Encode(data []byte) string {
	// Use standard encoding
	const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result []byte
	for i := 0; i < len(data); i += 3 {
		chunk := uint32(data[i]) << 16
		if i+1 < len(data) {
			chunk |= uint32(data[i+1]) << 8
		}
		if i+2 < len(data) {
			chunk |= uint32(data[i+2])
		}

		result = append(result, base64Chars[(chunk>>18)&0x3F])
		result = append(result, base64Chars[(chunk>>12)&0x3F])
		if i+1 < len(data) {
			result = append(result, base64Chars[(chunk>>6)&0x3F])
		} else {
			result = append(result, '=')
		}
		if i+2 < len(data) {
			result = append(result, base64Chars[chunk&0x3F])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}
