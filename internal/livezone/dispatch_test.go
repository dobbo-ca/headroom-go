package livezone

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/tidwall/gjson"
)

// compressibleLog is a repetitive log body the log compressor reliably
// shrinks. Kept well above BlockByteThreshold.
//
// Deviation from the plan: the plan's original body was pure "INFO" lines,
// which internal/detect classifies as PlainText (buildRe requires one of
// :\d+:\d+:, error:, warning:, FAILED, panic:, undefined:), so LogOffload
// (AppliesTo == BuildOutput) never ran and every Dispatch call fell through
// to "no_candidates". A leading FAILED line makes the corpus detect as
// BuildOutput, matching this helper's own doc comment, while the 200
// repetitive INFO lines still give the log compressor plenty to template
// away.
func compressibleLog() string {
	var b strings.Builder
	b.WriteString("FAILED build with 1 error\n")
	for i := 0; i < 200; i++ {
		b.WriteString("2026-01-01 00:00:00 INFO  worker: processed batch id=")
		b.WriteString(strings.Repeat("0", 6))
		b.WriteString(" status=ok latency_ms=12\n")
	}
	return b.String()
}

// bodyWith embeds text as the latest user message's single text block.
func bodyWith(text string) []byte {
	quoted, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return []byte(`{"model":"claude-3-5-sonnet-20241022","system":"you are helpful",` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"earlier turn"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ack"}]},` +
		`{"role":"user","content":[{"type":"text","text":` + string(quoted) + `}]}` +
		`]}`)
}

func liveOptions(t *testing.T) Options {
	t.Helper()
	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 100})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	return Options{
		Policy:      policy.ForMode(policy.PAYG),
		Router:      router.NewDefault(),
		Store:       store,
		FrozenCount: -1,
	}
}

// The headline test: a body that genuinely gets rewritten must still satisfy
// I1 on every untouched byte.
func TestDispatchRealCompressionPreservesUntouchedRanges(t *testing.T) {
	in := bodyWith(compressibleLog())
	res := Dispatch(in, liveOptions(t))

	if !res.Applied {
		t.Fatalf("expected compression to apply, got reason %q", res.Reason)
	}
	if len(res.Rewritten) == 0 {
		t.Fatal("Applied is true but no ranges were reported")
	}
	assertUntouchedRangesIdentical(t, in, res)
	if len(res.Body) >= len(in) {
		t.Errorf("output is %d bytes, not smaller than the %d-byte input", len(res.Body), len(in))
	}
}

// The output must still be valid JSON with the same shape outside the
// rewritten block.
func TestDispatchOutputIsValidJSON(t *testing.T) {
	in := bodyWith(compressibleLog())
	res := Dispatch(in, liveOptions(t))
	if !res.Applied {
		t.Fatalf("expected compression, got %q", res.Reason)
	}
	var v map[string]any
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if v["system"] != "you are helpful" {
		t.Errorf("system field changed: %v", v["system"])
	}
	if v["model"] != "claude-3-5-sonnet-20241022" {
		t.Errorf("model field changed: %v", v["model"])
	}
}

// I2/I3: the system field, the tools field, and every frozen message must be
// byte-identical in the output.
func TestDispatchNeverTouchesFrozenPrefix(t *testing.T) {
	in := bodyWith(compressibleLog())
	res := Dispatch(in, liveOptions(t))
	if !res.Applied {
		t.Fatalf("expected compression, got %q", res.Reason)
	}
	for _, frag := range []string{
		`"system":"you are helpful"`,
		`{"role":"user","content":[{"type":"text","text":"earlier turn"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"ack"}]}`,
	} {
		if !strings.Contains(string(res.Body), frag) {
			t.Errorf("frozen fragment was modified or removed: %s", frag)
		}
	}
	// Every rewritten range must lie strictly after the frozen prefix.
	prefixEnd := strings.Index(string(in), `"ack"`) + len(`"ack"`)
	for _, r := range res.Rewritten {
		if r.Start < prefixEnd {
			t.Errorf("range %+v starts inside the frozen prefix (ends at %d)", r, prefixEnd)
		}
	}
}

// A cache_control marker on the last user message freezes it, so there is no
// live zone left and the body must pass through byte-identical.
func TestDispatchRespectsCacheControlFloor(t *testing.T) {
	log := compressibleLog()
	quoted, _ := json.Marshal(log)
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":` +
		string(quoted) + `,"cache_control":{"type":"ephemeral"}}]}]}`)

	res := Dispatch(in, liveOptions(t))
	if res.Applied {
		t.Error("compressed a message that a cache_control marker had frozen")
	}
	if string(res.Body) != string(in) {
		t.Error("body changed despite the frozen floor")
	}
	if res.FrozenCount != 1 {
		t.Errorf("FrozenCount = %d, want 1", res.FrozenCount)
	}
	assertUntouchedRangesIdentical(t, in, res)
}

// I2: hot-zone block types inside the live zone are recorded but never
// rewritten, even when they are large and compressible.
func TestDispatchNeverCompressesHotZoneBlocks(t *testing.T) {
	log := compressibleLog()
	quoted, _ := json.Marshal(log)
	for _, blockType := range HotZoneBlockTypes {
		t.Run(blockType, func(t *testing.T) {
			in := []byte(`{"messages":[{"role":"user","content":[` +
				`{"type":"` + blockType + `","text":` + string(quoted) + `}]}]}`)

			res := Dispatch(in, liveOptions(t))
			if res.Applied {
				t.Errorf("compressed a %s block", blockType)
			}
			if string(res.Body) != string(in) {
				t.Errorf("%s block body changed", blockType)
			}
			var sawHot bool
			for _, b := range res.Blocks {
				if b.Action == "hot_zone" && b.BlockType == blockType {
					sawHot = true
				}
			}
			if !sawHot {
				t.Errorf("%s was not recorded as hot_zone in Blocks: %+v", blockType, res.Blocks)
			}
		})
	}
}

// Every emitted CCR marker must resolve to the original text in the store.
func TestDispatchEmittedMarkersResolve(t *testing.T) {
	original := compressibleLog()
	in := bodyWith(original)
	opts := liveOptions(t)

	res := Dispatch(in, opts)
	if !res.Applied {
		t.Fatalf("expected compression, got %q", res.Reason)
	}

	var found int
	for _, b := range res.Blocks {
		if b.Action != "compressed" {
			continue
		}
		found++
		if b.CacheKey == "" {
			t.Fatal("a compressed block has no CacheKey")
		}
		got, ok := opts.Store.Get(b.CacheKey)
		if !ok {
			t.Fatalf("CCR key %q does not resolve", b.CacheKey)
		}
		if got != original {
			t.Errorf("CCR payload does not match the original text")
		}
		if !strings.Contains(string(res.Body), ccr.MarkerFor(b.CacheKey)) {
			t.Errorf("marker for %q is absent from the output body", b.CacheKey)
		}
	}
	if found == 0 {
		t.Fatal("no compressed block found")
	}
}

// A block that every compressor declines forwards verbatim and stores nothing.
//
// This covers the Dispatch-level "no_op" branch: a real router runs, no
// compressor produces a change, and the body must come back byte-identical
// with an empty CCR store. The I5-rejection path is a different branch and is
// covered by TestDispatchRejectedBlockLeavesNoOrphanEntry, which forces equal
// token counts and asserts ReasonAllRejected.
func TestDispatchIncompressibleBlockForwardsVerbatim(t *testing.T) {
	// Random-looking text the compressors cannot shrink, above threshold.
	var b strings.Builder
	for i := 0; i < 700; i++ {
		b.WriteByte(byte('a' + (i*7+i*i*3)%26))
	}
	in := bodyWith(b.String())
	opts := liveOptions(t)

	res := Dispatch(in, opts)
	if res.Applied {
		t.Fatalf("this corpus must stay incompressible for the no_op branch to be covered; got reason %q", res.Reason)
	}
	// Assert the branch by name. Without this the test would silently pass on
	// any other passthrough path and stop covering no_op.
	if res.Reason != ReasonNoCandidates {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonNoCandidates)
	}
	var sawNoOp bool
	for _, bl := range res.Blocks {
		if bl.Action == "no_op" {
			sawNoOp = true
		}
	}
	if !sawNoOp {
		t.Errorf("no no_op outcome recorded; the compressors did something: %+v", res.Blocks)
	}
	if opts.Store.Len() != 0 {
		t.Errorf("store has %d entries after a no-op dispatch, want 0", opts.Store.Len())
	}
	if string(res.Body) != string(in) {
		t.Error("body changed on a no-op dispatch")
	}
	assertUntouchedRangesIdentical(t, in, res)
}

// A short block is skipped by the byte-threshold gate.
func TestDispatchBelowThresholdIsSkipped(t *testing.T) {
	in := bodyWith("short log line")
	res := Dispatch(in, liveOptions(t))
	if res.Applied {
		t.Error("compressed a block below the byte threshold")
	}
	var sawBelow bool
	for _, b := range res.Blocks {
		if b.Action == "below_threshold" {
			sawBelow = true
		}
	}
	if !sawBelow {
		t.Errorf("no below_threshold outcome recorded: %+v", res.Blocks)
	}
}

// An explicit FrozenCount must override the derived one.
func TestDispatchExplicitFrozenCountOverridesDerived(t *testing.T) {
	in := bodyWith(compressibleLog())
	opts := liveOptions(t)
	opts.FrozenCount = 3 // freezes all three messages

	res := Dispatch(in, opts)
	if res.Applied {
		t.Error("compressed despite an explicit frozen floor above every message")
	}
	if res.Reason != ReasonNoLiveZone {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonNoLiveZone)
	}
	if res.FrozenCount != 3 {
		t.Errorf("FrozenCount = %d, want the explicit 3", res.FrozenCount)
	}
}

// bodyTwoBlocks puts two independently compressible text blocks in the
// latest user message, with a verbatim gap between them.
func bodyTwoBlocks() []byte {
	first, err := json.Marshal(compressibleLog())
	if err != nil {
		panic(err)
	}
	second, err := json.Marshal(compressibleLog() + "tail\n")
	if err != nil {
		panic(err)
	}
	return []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":` + string(first) + `},` +
		`{"type":"text","text":"gap"},` +
		`{"type":"text","text":` + string(second) + `}` +
		`]}]}`)
}

// With more than one rewrite in a single body the ORDER of the reported
// ranges and outcomes becomes observable. Both must be ascending and
// non-overlapping: anything that reaches the output path through a map
// (or drops the splice sort) shows up here and nowhere else, because every
// other end-to-end body rewrites at most one block.
func TestDispatchMultipleBlocksAreOrderedAndSpliced(t *testing.T) {
	in := bodyTwoBlocks()
	opts := liveOptions(t)
	res := Dispatch(in, opts)

	if !res.Applied {
		t.Fatalf("expected compression, got %q", res.Reason)
	}
	if len(res.Rewritten) != 2 {
		t.Fatalf("rewrote %d ranges, want 2: %+v", len(res.Rewritten), res.Rewritten)
	}
	if res.Rewritten[0].Start >= res.Rewritten[1].Start {
		t.Errorf("ranges are not ascending: %+v", res.Rewritten)
	}
	if res.Rewritten[0].End > res.Rewritten[1].Start {
		t.Errorf("ranges overlap: %+v", res.Rewritten)
	}

	// Blocks carry every content block, in content order.
	wantIdx := []int{0, 1, 2}
	wantAct := []string{"compressed", "below_threshold", "compressed"}
	if len(res.Blocks) != len(wantIdx) {
		t.Fatalf("Blocks = %+v, want %d entries", res.Blocks, len(wantIdx))
	}
	for i, b := range res.Blocks {
		if b.Index != wantIdx[i] || b.Action != wantAct[i] {
			t.Errorf("Blocks[%d] = {Index:%d Action:%q}, want {Index:%d Action:%q}",
				i, b.Index, b.Action, wantIdx[i], wantAct[i])
		}
	}

	assertUntouchedRangesIdentical(t, in, res)

	// The untouched gap block survives verbatim and the output still parses.
	if !strings.Contains(string(res.Body), `{"type":"text","text":"gap"}`) {
		t.Error("the gap block between the two rewrites was not copied verbatim")
	}
	var v map[string]any
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Each block's marker resolves to that block's own original: two
	// rewrites in one body must not share or cross their CCR keys.
	for i, b := range res.Blocks {
		if b.Action != "compressed" {
			continue
		}
		got, ok := opts.Store.Get(b.CacheKey)
		if !ok {
			t.Fatalf("Blocks[%d]: key %q does not resolve", i, b.CacheKey)
		}
		if want := gjson.GetBytes(in, fmt.Sprintf("messages.0.content.%d.text", b.Index)).Str; got != want {
			t.Errorf("Blocks[%d]: store holds the wrong original for key %q", i, b.CacheKey)
		}
	}
}

// awkwardBodyWith embeds text as the latest user message's text block inside a
// body whose UNTOUCHED regions are deliberately hostile to re-serialisation:
// irregular whitespace, non-alphabetical key order, numeric spellings that
// json.Marshal would rewrite (1.50 -> 1.5, 1e2 -> 100, 0.500 -> 0.5), unicode,
// and characters json.Marshal escapes by default (< > &).
//
// bodyWith emits compact JSON, so a splice bug that disturbed the untouched
// bytes could not show up there. This fixture is what makes that bug visible.
func awkwardBodyWith(text string) []byte {
	quoted, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return []byte("{\n" +
		"  \"temperature\" : 1.50 ,\n" +
		"  \"system\"      : \"café — you are helpful, a < b && c > d\" ,\n" +
		"  \"max_tokens\"  : 1e2 ,\n" +
		"  \"messages\"    : [\n" +
		"      { \"content\" : [ { \"type\" : \"text\" , \"text\" : \"earlier turn\" } ] , \"role\" : \"user\" } ,\n" +
		"      { \"role\" : \"assistant\" , \"content\" : [ { \"type\" : \"text\" , \"text\" : \"ack\" } ] } ,\n" +
		"      { \"role\" : \"user\" , \"content\" : [ { \"type\" : \"text\" , \"text\" : " + string(quoted) + " } ] }\n" +
		"  ] ,\n" +
		"  \"top_p\"       : 0.500\n" +
		"}\n")
}

// I1 under real compression, on a body whose untouched regions would not
// survive a round trip through encoding/json. Any splice that regenerates a
// gap instead of copying it fails here.
func TestDispatchPreservesAwkwardFormattingAroundACompressedBlock(t *testing.T) {
	in := awkwardBodyWith(compressibleLog())
	res := Dispatch(in, liveOptions(t))

	if !res.Applied {
		t.Fatalf("fixture must actually compress, got reason %q", res.Reason)
	}
	if len(res.Rewritten) == 0 {
		t.Fatal("Applied is true but no ranges were reported")
	}
	assertUntouchedRangesIdentical(t, in, res)

	// Every fragment below sits OUTSIDE the rewritten range and must survive
	// byte for byte. json.Marshal would alter each one.
	out := string(res.Body)
	for _, frag := range []string{
		"  \"temperature\" : 1.50 ,\n",               // trailing zero and spacing
		"  \"max_tokens\"  : 1e2 ,\n",                // exponent form
		"  \"top_p\"       : 0.500\n",                // trailing zeros
		"\"café — you are helpful, a < b && c > d\"", // unicode plus unescaped < > &
		"{ \"content\" : [ { \"type\" : \"text\" , \"text\" : \"earlier turn\" } ] , \"role\" : \"user\" }", // content-before-role
		"{ \"role\" : \"assistant\" , \"content\" : [ { \"type\" : \"text\" , \"text\" : \"ack\" } ] }",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("untouched formatting was not preserved verbatim:\n  want fragment: %q", frag)
		}
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Error("the trailing gap after the last replacement was not copied verbatim")
	}
}

// The same guarantee for the trailing region specifically: bytes after the
// last rewritten range must be copied, not regenerated.
func TestDispatchPreservesTrailingBytesAfterTheLastReplacement(t *testing.T) {
	in := awkwardBodyWith(compressibleLog())
	res := Dispatch(in, liveOptions(t))
	if !res.Applied {
		t.Fatalf("fixture must actually compress, got reason %q", res.Reason)
	}

	last := res.Rewritten[len(res.Rewritten)-1]
	wantTail := in[last.End:]
	// Locate the same tail in the output by reconstructing the cursor.
	outPos := 0
	inPos := 0
	for _, r := range res.Rewritten {
		outPos += (r.Start - inPos) + r.NewLen
		inPos = r.End
	}
	gotTail := res.Body[outPos:]
	if string(gotTail) != string(wantTail) {
		t.Errorf("trailing bytes differ\n want: %q\n got:  %q", wantTail, gotTail)
	}
}
