package cachestab

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sortedJoin renders drifted dimensions in a stable order for comparison.
func sortedJoin(dims []string) string {
	out := append([]string(nil), dims...)
	sort.Strings(out)
	return strings.Join(out, ",")
}

// turn builds an Anthropic body. markLast places a cache_control breakpoint on
// the final message, which is what Claude Code does on every turn.
func turn(system string, tools string, msgs []string, markLast bool) []byte {
	var b bytes.Buffer
	b.WriteString(`{"model":"claude-sonnet-5","system":` + quote(system) + `,"tools":` + tools + `,"messages":[`)
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"user","content":[{"type":"text","text":` + quote(m))
		if markLast && i == len(msgs)-1 {
			b.WriteString(`,"cache_control":{"type":"ephemeral"}`)
		}
		b.WriteString(`}]}`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

const twoTools = `[{"name":"a","input_schema":{"type":"object"}},{"name":"b","input_schema":{"type":"object"}}]`

func TestStructuralHashIsStableAcrossIdenticalBodies(t *testing.T) {
	a := ComputeStructuralHash(turn("sys", twoTools, []string{"one", "two"}, true))
	b := ComputeStructuralHash(turn("sys", twoTools, []string{"one", "two"}, true))
	if dims := a.Drifted(b); len(dims) != 0 {
		t.Errorf("identical bodies drifted on %v", dims)
	}
}

// Whitespace and key order are formatter noise, not drift. Operators care
// about semantic change.
func TestStructuralHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a := ComputeStructuralHash([]byte(`{"system":{"x":1,"y":2},"tools":[],"messages":[]}`))
	b := ComputeStructuralHash([]byte("{\n  \"system\" : { \"y\" : 2 , \"x\" : 1 } ,\n \"tools\":[], \"messages\":[]}"))
	if dims := a.Drifted(b); len(dims) != 0 {
		t.Errorf("key order or whitespace read as drift on %v", dims)
	}
}

// THE load-bearing case. Claude Code moves its cache breakpoint to the newest
// message every single turn. If a relocated breakpoint counted as drift, the
// detector would warn on every request and be worthless.
func TestRelocatedCacheBreakpointIsNotDrift(t *testing.T) {
	// Turn one pins message one. Turn two appends and pins message two.
	one := ComputeStructuralHash(turn("sys", twoTools, []string{"one"}, true))
	two := ComputeStructuralHash(turn("sys", twoTools, []string{"one", "two"}, true))

	if dims := one.Drifted(two); len(dims) != 0 {
		t.Errorf("relocating the cache breakpoint and appending read as drift on %v; "+
			"moving a breakpoint never invalidates an already-cached prefix", dims)
	}
	// Prove the marker really did move, so this test cannot pass vacuously.
	if bytes.Equal(turn("sys", twoTools, []string{"one"}, true),
		turn("sys", twoTools, []string{"one"}, false)) {
		t.Fatal("the fixture does not actually place a cache_control marker")
	}
}

// A cache_control key inside an opaque payload is the customer's own data.
// Stripping it would make two different payloads hash alike and hide drift.
func TestCacheControlInsideOpaquePayloadIsStructural(t *testing.T) {
	a := ComputeStructuralHash([]byte(
		`{"messages":[{"role":"user","content":[{"type":"tool_use","input":{"cache_control":"alpha"}}]}]}`))
	b := ComputeStructuralHash([]byte(
		`{"messages":[{"role":"user","content":[{"type":"tool_use","input":{"cache_control":"beta"}}]}]}`))
	if dims := a.Drifted(b); len(dims) == 0 {
		t.Error("a changed cache_control VALUE inside a tool input was ignored; " +
			"inside an opaque payload it is data, not a breakpoint")
	}
}

// Growth into an empty early slot is append-only and benign; a settled slot
// changing means the provider's cached prefix was rewritten.
func TestEarlyMessagesGrowthIsBenignButRewriteIsDrift(t *testing.T) {
	one := ComputeStructuralHash(turn("s", twoTools, []string{"a"}, false))
	grown := ComputeStructuralHash(turn("s", twoTools, []string{"a", "b", "c"}, false))
	if dims := one.Drifted(grown); len(dims) != 0 {
		t.Errorf("appending messages read as drift on %v", dims)
	}

	rewritten := ComputeStructuralHash(turn("s", twoTools, []string{"CHANGED", "b", "c"}, false))
	if dims := grown.Drifted(rewritten); len(dims) != 1 || dims[0] != "early_messages" {
		t.Errorf("rewriting a settled early message gave %v, want [early_messages]", dims)
	}

	shrunk := ComputeStructuralHash(turn("s", twoTools, []string{"a"}, false))
	if dims := grown.Drifted(shrunk); len(dims) != 1 || dims[0] != "early_messages" {
		t.Errorf("dropping settled messages gave %v, want [early_messages]", dims)
	}
}

// Past the window is the live zone, where mutation is expected. Reporting it
// would mean warning on every turn.
func TestChangesPastTheEarlyWindowAreIgnored(t *testing.T) {
	base := []string{"a", "b", "c", "d", "e"}
	changed := []string{"a", "b", "c", "d", "TOTALLY DIFFERENT"}
	a := ComputeStructuralHash(turn("s", twoTools, base, false))
	b := ComputeStructuralHash(turn("s", twoTools, changed, false))
	if dims := a.Drifted(b); len(dims) != 0 {
		t.Errorf("a change past message %d read as drift on %v", EarlyMessagesWindow, dims)
	}
}

func TestSystemAndToolsDriftAreReportedSeparately(t *testing.T) {
	base := turn("sys", twoTools, []string{"a"}, false)
	a := ComputeStructuralHash(base)

	sysChanged := ComputeStructuralHash(turn("DIFFERENT", twoTools, []string{"a"}, false))
	if dims := a.Drifted(sysChanged); len(dims) != 1 || dims[0] != "system" {
		t.Errorf("system change gave %v, want [system]", dims)
	}

	oneTool := `[{"name":"a","input_schema":{"type":"object"}}]`
	toolsChanged := ComputeStructuralHash(turn("sys", oneTool, []string{"a"}, false))
	if dims := a.Drifted(toolsChanged); len(dims) != 1 || dims[0] != "tools" {
		t.Errorf("tools change gave %v, want [tools]", dims)
	}

	both := ComputeStructuralHash(turn("DIFFERENT", oneTool, []string{"CHANGED"}, false))
	if got := sortedJoin(a.Drifted(both)); got != "early_messages,system,tools" {
		t.Errorf("all three changed gave %q, want every axis", got)
	}
}

// Removing a field must be distinguishable from leaving it unchanged.
func TestRemovingSystemIsDrift(t *testing.T) {
	with := ComputeStructuralHash([]byte(`{"system":"s","messages":[]}`))
	without := ComputeStructuralHash([]byte(`{"messages":[]}`))
	if dims := with.Drifted(without); len(dims) != 1 || dims[0] != "system" {
		t.Errorf("dropping system gave %v, want [system]", dims)
	}
}

func TestComputeStructuralHashNeverMutatesInput(t *testing.T) {
	body := turn("sys", twoTools, []string{"a", "b"}, true)
	before := append([]byte(nil), body...)
	ComputeStructuralHash(body)
	if !bytes.Equal(body, before) {
		t.Error("ComputeStructuralHash altered the caller's buffer")
	}
}

func TestObserveReportsFirstRequestThenDrift(t *testing.T) {
	st := NewDriftState(8)
	base := ComputeStructuralHash(turn("sys", twoTools, []string{"a"}, false))

	first := st.Observe("session-1", base)
	if !first.FirstRequest {
		t.Error("the first observation of a session must be flagged as such")
	}
	if first.Drifted() {
		t.Error("a first request cannot have drifted; there is no baseline")
	}
	if first.SessionDigest == "session-1" {
		t.Error("the raw session key leaked into the observation")
	}

	same := st.Observe("session-1", base)
	if same.FirstRequest || same.Drifted() {
		t.Errorf("an unchanged prefix reported first=%v dims=%v", same.FirstRequest, same.Dims)
	}

	moved := ComputeStructuralHash(turn("CHANGED", twoTools, []string{"a"}, false))
	drifted := st.Observe("session-1", moved)
	if !drifted.Drifted() || drifted.Dims[0] != "system" {
		t.Errorf("a busted prefix reported drifted=%v dims=%v", drifted.Drifted(), drifted.Dims)
	}

	// The baseline must advance, or every later turn re-reports the same drift.
	if again := st.Observe("session-1", moved); again.Drifted() {
		t.Errorf("the baseline did not advance; drift re-reported as %v", again.Dims)
	}
}

func TestObserveKeepsSessionsIndependent(t *testing.T) {
	st := NewDriftState(8)
	a := ComputeStructuralHash(turn("alpha", twoTools, []string{"a"}, false))
	b := ComputeStructuralHash(turn("beta", twoTools, []string{"a"}, false))

	st.Observe("one", a)
	st.Observe("two", b)
	if obs := st.Observe("one", a); obs.Drifted() {
		t.Errorf("session one drifted on %v after session two was observed", obs.Dims)
	}
}

func TestDriftStateEvictsAtCapacity(t *testing.T) {
	st := NewDriftState(3)
	h := ComputeStructuralHash(turn("s", twoTools, []string{"a"}, false))
	for i := 0; i < 10; i++ {
		st.Observe("session-"+strconv.Itoa(i), h)
	}
	if n := st.Len(); n != 3 {
		t.Errorf("state holds %d sessions, want the capacity of 3", n)
	}
	// The oldest was evicted, so it reads as a first request again.
	if obs := st.Observe("session-0", h); !obs.FirstRequest {
		t.Error("the oldest session was not evicted")
	}
}

func TestDriftStateIsSafeUnderConcurrentUse(t *testing.T) {
	st := NewDriftState(64)
	h := ComputeStructuralHash(turn("s", twoTools, []string{"a"}, false))
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			for j := 0; j < 50; j++ {
				st.Observe("session-"+strconv.Itoa(n), h)
			}
			close(done)
		}(i)
		<-done
		done = make(chan struct{})
	}
	if st.Len() == 0 {
		t.Error("no sessions recorded")
	}
}
