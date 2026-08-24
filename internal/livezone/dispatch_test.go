package livezone

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/router"
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

// A rejected block must leave no orphan entry in the store.
func TestDispatchRejectedBlockLeavesNoOrphan(t *testing.T) {
	// Random-looking text the compressors cannot shrink, above threshold.
	var b strings.Builder
	for i := 0; i < 700; i++ {
		b.WriteByte(byte('a' + (i*7+i*i*3)%26))
	}
	in := bodyWith(b.String())
	opts := liveOptions(t)

	res := Dispatch(in, opts)
	if res.Applied {
		t.Skip("this corpus turned out to be compressible; the orphan check needs a rejection")
	}
	if opts.Store.Len() != 0 {
		t.Errorf("store has %d entries after a rejected dispatch, want 0", opts.Store.Len())
	}
	if string(res.Body) != string(in) {
		t.Error("body changed on a rejected dispatch")
	}
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
