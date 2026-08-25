package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/tidwall/gjson"
)

// claudeCodeTurn builds a conversation in the shape Claude Code actually
// sends: a tool_result at message index 2, extraTurns appended pairs, and a
// cache_control marker on the NEWEST message.
//
// That marker is the crux of hr-fgo. It is a cache WRITE instruction for bytes
// the provider has never seen, but cachecontrol.ComputeFrozenCount reads it as
// "everything up to here is already cached", which freezes the whole
// conversation and leaves headroom nothing to do.
func claudeCodeTurn(t *testing.T, text string, extraTurns int) string {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"model":"claude-sonnet-5","system":"you are helpful","messages":[`)
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"run the build"}]},`)
	b.WriteString(`{"role":"assistant","content":[{"type":"text","text":"ack"}]},`)
	b.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":`)
	b.Write(quoted)
	b.WriteString(`}]}`)
	for i := 0; i < extraTurns; i++ {
		b.WriteString(`,{"role":"assistant","content":[{"type":"text","text":"looking"}]}`)
		b.WriteString(`,{"role":"user","content":[{"type":"text","text":"carry on"}]}`)
	}
	b.WriteString(`]}`)

	// Mark the newest message, exactly as the client does.
	body := b.String()
	last := 2 + 2*extraTurns
	old := `{"role":"user","content":[{"type":"text","text":"carry on"}]}`
	if extraTurns > 0 {
		marked := `{"role":"user","content":[{"type":"text","text":"carry on","cache_control":{"type":"ephemeral"}}]}`
		body = strings.TrimSuffix(body, old+`]}`) + marked + `]}`
	} else {
		body = strings.Replace(body,
			`"content":`+string(quoted)+`}]}]}`,
			`"content":`+string(quoted)+`,"cache_control":{"type":"ephemeral"}}]}]}`, 1)
	}
	if gjson.Get(body, "messages."+strconv.Itoa(last)+".content.0.cache_control").Exists() != true {
		t.Fatalf("fixture failed to mark the newest message (index %d); it no longer models Claude Code", last)
	}
	return body
}

// replayRig posts turns through a real proxy and records every body the
// upstream received.
type replayRig struct {
	front *httptest.Server
	mu    sync.Mutex
	sent  [][]byte
}

func newReplayRig(t *testing.T, replay bool) *replayRig {
	t.Helper()
	rig := &replayRig{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rig.mu.Lock()
		rig.sent = append(rig.sent, b)
		rig.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	t.Cleanup(up.Close)

	srv := New(Deps{
		Config: Config{
			Upstream: up.URL, MaxBodyBytes: 32 << 20, Compress: true,
			RequestTimeout: 30e9, DialTimeout: 10e9, Replay: replay,
		},
		Store:     newMapStore(),
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "replay-test",
	})
	rig.front = httptest.NewServer(srv.Handler())
	t.Cleanup(rig.front.Close)
	return rig
}

// post sends one turn under a fixed session id, so both turns land on the same
// replay session the way a real client's do.
func (rig *replayRig) post(t *testing.T, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rig.front.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "11111111-2222-3333-4444-555555555555")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// prefixThroughMessage2 is the span the provider's prompt cache covers once
// message 2 is inside a marked prefix.
func prefixThroughMessage2(t *testing.T, body []byte) []byte {
	t.Helper()
	m := gjson.GetBytes(body, "messages.2")
	if !m.Exists() || m.Index <= 0 {
		t.Fatalf("messages.2 has no usable byte offset in a %d-byte body", len(body))
	}
	return body[:m.Index+len(m.Raw)]
}

// The status quo, pinned. With replay off, a Claude Code-shaped request is
// forwarded byte-for-byte on every turn: the cache_control marker on the
// newest message freezes the whole conversation and headroom saves nothing.
//
// This is the negative control for the test below. If it starts failing,
// something began mutating bodies without replay to keep them stable.
func TestProxyWithoutReplayForwardsClaudeCodeTurnsVerbatim(t *testing.T) {
	rig := newReplayRig(t, false)
	log := compressibleLog()
	turn1, turn2 := claudeCodeTurn(t, log, 0), claudeCodeTurn(t, log, 1)

	rig.post(t, turn1)
	rig.post(t, turn2)

	if len(rig.sent) != 2 {
		t.Fatalf("upstream saw %d bodies, want 2", len(rig.sent))
	}
	for i, want := range []string{turn1, turn2} {
		if string(rig.sent[i]) != want {
			t.Errorf("turn %d was modified with replay off: %d bytes in, %d out",
				i+1, len(want), len(rig.sent[i]))
		}
	}
}

// THE END-TO-END PROOF of hr-fgo. With replay on, the same two turns give the
// upstream a SMALLER turn 1 and a turn 2 whose cached prefix is byte-identical
// to it — so the saving is real and it does not cost a cache miss per turn.
func TestProxyWithReplayShrinksTurnOneAndKeepsTurnTwoPrefixIdentical(t *testing.T) {
	rig := newReplayRig(t, true)
	log := compressibleLog()
	turn1, turn2 := claudeCodeTurn(t, log, 0), claudeCodeTurn(t, log, 1)

	rig.post(t, turn1)
	rig.post(t, turn2)

	if len(rig.sent) != 2 {
		t.Fatalf("upstream saw %d bodies, want 2", len(rig.sent))
	}
	if len(rig.sent[0]) >= len(turn1) {
		t.Fatalf("turn 1 was not compressed: %d bytes in, %d out — the frozen floor is still "+
			"being read off the client's cache_control marker", len(turn1), len(rig.sent[0]))
	}
	t.Logf("turn 1: client sent %d bytes, upstream received %d (%.1f%% cut)",
		len(turn1), len(rig.sent[0]), 100*(1-float64(len(rig.sent[0]))/float64(len(turn1))))

	// The client moves its cache_control breakpoint to the newest block on
	// every turn. That is Anthropic's own recommended pattern and it does
	// NOT invalidate an already-cached prefix, so it is the one difference
	// that must be discounted. Exactly that literal is removed and nothing
	// else, so any byte headroom itself changed still shows up.
	prefix := withoutBreakpoints(prefixThroughMessage2(t, rig.sent[0]))
	turn2Prefix := withoutBreakpoints(prefixThroughMessage2(t, rig.sent[1]))
	if !bytes.Equal(prefix, turn2Prefix) {
		t.Fatalf("turn 2 rewrote the prefix the provider cached on turn 1; every turn now pays "+
			"a fresh cache write\nturn 1 prefix: %d bytes\nturn 2 prefix: %d bytes\nfirst delta at: %d",
			len(prefix), len(turn2Prefix), firstDelta(prefix, turn2Prefix))
	}
	// Discounting the breakpoint must not be what makes this pass: the
	// prefix has to be the COMPRESSED form, not the client's own bytes.
	if bytes.Equal(prefix, withoutBreakpoints(prefixThroughMessage2(t, []byte(turn1)))) {
		t.Fatal("turn 1's prefix is the client's own bytes: nothing was compressed, so the " +
			"stability claim is vacuous")
	}
	// And the compressed block itself must be identical, breakpoint or no
	// breakpoint. This is the span headroom owns.
	if a, b := replayedBlockText(rig.sent[0]), replayedBlockText(rig.sent[1]); a == "" || a != b {
		t.Fatalf("the compressed tool_result differs between turns:\nturn 1: %q\nturn 2: %q", a, b)
	}
	for _, b := range rig.sent {
		if !json.Valid(b) {
			t.Error("the proxy sent invalid JSON upstream")
		}
	}
}

// withoutBreakpoints removes the client's cache_control markers and nothing
// else. Placement metadata is not part of the cached content: Anthropic's
// documented pattern is to move the breakpoint to the newest block each turn,
// which would be impossible if doing so dropped the hit.
func withoutBreakpoints(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte(`,"cache_control":{"type":"ephemeral"}`), nil)
}

// replayedBlockText returns the text of the tool_result block at message 2 —
// the block headroom compresses on turn 1 and must replay on every turn after.
func replayedBlockText(body []byte) string {
	return gjson.GetBytes(body, "messages.2.content.0.content").Str
}

// firstDelta reports the byte offset where a and b diverge, for a failure
// message that names the spot instead of dumping two buffers.
func firstDelta(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
