package livezone

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// assertUntouchedRangesIdentical is invariant I1, expressed so it survives
// every later task. It walks the input and output in lockstep: each gap
// between rewritten ranges must be SHA-256-identical, and the rewritten
// ranges themselves are skipped by their respective lengths.
//
// With no rewrites this degenerates to "the whole body is unchanged". With
// rewrites it still proves every byte outside them was copied verbatim.
func assertUntouchedRangesIdentical(t *testing.T, in []byte, res Result) {
	t.Helper()

	inPos, outPos := 0, 0
	for i, r := range res.Rewritten {
		if r.Start < inPos {
			t.Fatalf("range %d starts at %d, before cursor %d: ranges must be sorted and non-overlapping", i, r.Start, inPos)
		}
		if r.End < r.Start || r.End > len(in) {
			t.Fatalf("range %d is [%d,%d), out of bounds for a %d-byte input", i, r.Start, r.End, len(in))
		}
		gap := r.Start - inPos
		if outPos+gap > len(res.Body) {
			t.Fatalf("range %d: output is too short to hold the gap before it", i)
		}
		inGap := in[inPos : inPos+gap]
		outGap := res.Body[outPos : outPos+gap]
		if sum(inGap) != sum(outGap) {
			t.Fatalf("range %d: untouched bytes [%d,%d) were modified\n in: %q\nout: %q",
				i, inPos, r.Start, inGap, outGap)
		}
		inPos = r.End
		outPos += gap + r.NewLen
	}

	inTail, outTail := in[inPos:], res.Body[outPos:]
	if sum(inTail) != sum(outTail) {
		t.Fatalf("trailing untouched bytes from %d were modified\n in: %q\nout: %q", inPos, inTail, outTail)
	}
	if outPos+len(inTail) != len(res.Body) {
		t.Fatalf("output length %d does not match the reconstruction %d", len(res.Body), outPos+len(inTail))
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Corpus of Anthropic-shaped bodies with deliberately awkward formatting:
// odd whitespace, non-alphabetical key order, unusual numeric spellings, and
// unicode. Re-serialising any of these would change the bytes, so this
// corpus is what makes I1 a real assertion rather than a tautology.
func roundTripCorpus() map[string]string {
	return map[string]string{
		"minimal": `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`,

		"irregular whitespace": "{\n  \"model\" : \"claude-3\",\n\t\"messages\" : [\n\t\t{ \"role\" : \"user\" , \"content\" : \"hi\" }\n  ]\n}",

		"non alphabetical key order": `{"messages":[{"content":"hi","role":"user"}],"system":"s","model":"claude-3"}`,

		// 1.50 and 1e2 must survive: json.Marshal would rewrite them as
		// 1.5 and 100.
		"numeric formatting": `{"temperature":1.50,"max_tokens":1e2,"top_p":0.500,"messages":[{"role":"user","content":"hi"}]}`,

		"unicode and escapes": `{"messages":[{"role":"user","content":"café — \"quoted\" \\ backslash éè"}]}`,

		"html chars that Marshal would escape": `{"messages":[{"role":"user","content":"a < b && c > d"}]}`,

		"block content": `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,

		"hot zone blocks only": `{"messages":[{"role":"user","content":[{"type":"thinking","thinking":"deep"},{"type":"tool_use","id":"t1","name":"x","input":{}}]}]}`,

		"with system and tools": `{"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"t","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`,

		"multi turn with markers": `{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":[{"type":"text","text":"b"}]},{"role":"user","content":[{"type":"text","text":"c"}]}]}`,

		"empty messages": `{"messages":[]}`,

		"trailing newline": "{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}\n",
	}
}

// I1, red test #1: with no compressor wired, every byte must survive.
func TestDispatchPreservesUntouchedRanges(t *testing.T) {
	for name, body := range roundTripCorpus() {
		t.Run(name, func(t *testing.T) {
			in := []byte(body)
			res := Dispatch(in, Options{FrozenCount: -1})
			assertUntouchedRangesIdentical(t, in, res)
		})
	}
}

// With no Router wired there is nothing to compress, so the body must come
// back byte-identical, not merely equivalent.
func TestDispatchWithoutRouterIsByteIdentical(t *testing.T) {
	for name, body := range roundTripCorpus() {
		t.Run(name, func(t *testing.T) {
			in := []byte(body)
			res := Dispatch(in, Options{FrozenCount: -1})
			if res.Applied {
				t.Errorf("Applied = true with no Router wired")
			}
			if sum(res.Body) != sum(in) {
				t.Errorf("body changed with no Router wired\n in: %q\nout: %q", in, res.Body)
			}
		})
	}
}

// Dispatch must never hand back a nil body, whatever the input. A caller
// that forwards Result.Body unconditionally must be safe.
func TestDispatchNeverReturnsNilBody(t *testing.T) {
	inputs := [][]byte{
		nil,
		{},
		[]byte(`{not json`),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`{"messages":"oops"}`),
		[]byte(`{"model":"claude-3"}`),
	}
	for _, in := range inputs {
		t.Run(string(in), func(t *testing.T) {
			res := Dispatch(in, Options{FrozenCount: -1})
			if res.Body == nil && in != nil {
				t.Fatal("Dispatch returned a nil Body for a non-nil input")
			}
			if res.Applied {
				t.Errorf("Applied = true for input %q", in)
			}
			assertUntouchedRangesIdentical(t, in, res)
		})
	}
}

func TestDispatchReasonsOnBadInput(t *testing.T) {
	tests := []struct {
		body string
		want Reason
	}{
		{`{not json`, ReasonNotJSON},
		{`null`, ReasonNotJSON},
		{`[]`, ReasonNotJSON},
		{`{"model":"claude-3"}`, ReasonNoMessages},
		{`{"messages":"oops"}`, ReasonNoMessages},
		{`{"messages":[]}`, ReasonNoLiveZone},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			if got := Dispatch([]byte(tt.body), Options{FrozenCount: -1}).Reason; got != tt.want {
				t.Errorf("Reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// The Reason wire strings are what a caller logs and what an operator greps
// for, so they are pinned by literal, not by the constant that defines them.
func TestReasonWireValues(t *testing.T) {
	for got, want := range map[Reason]string{
		ReasonOK:           "ok",
		ReasonNotJSON:      "not_json",
		ReasonNoMessages:   "no_messages",
		ReasonNoLiveZone:   "no_live_zone",
		ReasonNoCandidates: "no_candidates",
		ReasonAllRejected:  "all_rejected",
	} {
		if string(got) != want {
			t.Errorf("Reason = %q, want %q", got, want)
		}
	}
}

// FrozenCount: -1 must actually derive the floor from the body's
// cache_control markers, and any other value must be honoured verbatim.
func TestDispatchFrozenCount(t *testing.T) {
	marked := []byte(roundTripCorpus()["multi turn with markers"])

	if got := Dispatch(marked, Options{FrozenCount: -1}).FrozenCount; got != 1 {
		t.Errorf("derived FrozenCount = %d, want 1", got)
	}
	if got := Dispatch([]byte(roundTripCorpus()["minimal"]), Options{FrozenCount: -1}).FrozenCount; got != 0 {
		t.Errorf("derived FrozenCount with no markers = %d, want 0", got)
	}
	if got := Dispatch(marked, Options{FrozenCount: 3}).FrozenCount; got != 3 {
		t.Errorf("explicit FrozenCount = %d, want 3 (not re-derived)", got)
	}
}

// I2: a user message frozen by a cache_control marker is out of the live
// zone. The floor must reach findLatestUserMessage — with the marked message
// frozen there is no live zone at all, and Dispatch must say so rather than
// plan the frozen message's blocks as candidates.
func TestDispatchHonoursFrozenFloor(t *testing.T) {
	// Message 0 is a marked, comfortably compressible user message;
	// message 1 is an assistant turn, so nothing above the floor is live.
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + big(600) + `","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"assistant","content":"ok"}]}`)

	if got := Dispatch(body, Options{FrozenCount: -1}); got.FrozenCount != 1 || got.Reason != ReasonNoLiveZone {
		t.Errorf("derived floor: FrozenCount = %d, Reason = %q; want 1, %q",
			got.FrozenCount, got.Reason, ReasonNoLiveZone)
	}
	if got := Dispatch(body, Options{FrozenCount: 1}).Reason; got != ReasonNoLiveZone {
		t.Errorf("explicit floor 1: Reason = %q, want %q", got, ReasonNoLiveZone)
	}
	// Drop the floor and the same message becomes the live zone, which
	// proves the floor is what excluded it above.
	if got := Dispatch(body, Options{FrozenCount: 0}).Reason; got != ReasonNoCandidates {
		t.Errorf("floor 0: Reason = %q, want %q", got, ReasonNoCandidates)
	}
}
