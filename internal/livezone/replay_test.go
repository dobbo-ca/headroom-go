package livezone

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/tidwall/gjson"
)

// replayBody builds a conversation carrying text as a tool_result block at
// message index 2, plus extraTurns appended message pairs.
//
// Everything up to the end of message 2 is byte-identical for every value of
// extraTurns, because later turns are APPENDED. That is exactly how a real
// client grows a conversation, and it is what makes a byte-prefix assertion
// across two turns meaningful.
func replayBody(text string, extraTurns int) []byte {
	quoted, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	b.WriteString(`{"model":"claude-3-5-sonnet-20241022","system":"you are helpful","messages":[`)
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
	return []byte(b.String())
}

// messagesPrefix returns body's bytes up to and including message n — the span
// the provider's prompt cache is keyed on once that message is marked.
func messagesPrefix(t *testing.T, body []byte, n int) []byte {
	t.Helper()
	m := gjson.GetBytes(body, "messages."+strconv.Itoa(n))
	if !m.Exists() || m.Index <= 0 {
		t.Fatalf("messages.%d has no usable byte offset", n)
	}
	return body[:m.Index+len(m.Raw)]
}

// twoTurns runs turn 1 (floor 0, everything fair game) then turn 2 (floor 3,
// so the fresh pass cannot reach message 2) over the same conversation, and
// returns both outputs. A nil-returning replayFn models the naive fix.
func twoTurns(t *testing.T, withReplay bool) (turn1, turn2, in1, in2 []byte) {
	t.Helper()

	opts := liveOptions(t)
	var handle *cachestab.SessionReplay
	if withReplay {
		handle = cachestab.NewReplayState(8).Begin("s", nil)
	}

	log := compressibleLog()

	in1 = replayBody(log, 0)
	opts.FrozenCount = 0
	opts.Replay = handle
	res1 := Dispatch(in1, opts)
	if !res1.Applied || res1.Reason != ReasonOK {
		t.Fatalf("turn 1 must compress; got applied=%v reason=%q", res1.Applied, res1.Reason)
	}

	// Turn 2 re-sends message 2 in its ORIGINAL form — the client never
	// learned headroom compressed it — and adds two new messages.
	in2 = replayBody(log, 1)
	opts.FrozenCount = 3
	res2 := Dispatch(in2, opts)
	return res1.Body, res2.Body, in1, in2
}

// THE HEADLINE TEST. The whole point of replay: the byte prefix the provider
// cached on turn 1 must arrive unchanged on turn 2, even though the client
// re-sent that message uncompressed and it now sits below the frozen floor.
func TestReplayKeepsTheCachedPrefixByteIdenticalAcrossTurns(t *testing.T) {
	turn1, turn2, in1, _ := twoTurns(t, true)

	prefix := messagesPrefix(t, turn1, 2)
	if !bytes.HasPrefix(turn2, prefix) {
		t.Fatalf("turn 2 rewrote the cached prefix; the provider cache is busted at or before message 2\n"+
			"turn 1 prefix (%d bytes) ends: %q\nturn 2 at that offset:      %q",
			len(prefix), tail(prefix, 80), tail(turn2[:min(len(turn2), len(prefix))], 80))
	}

	// Guard against a vacuous pass: the prefix must be the COMPRESSED form,
	// not simply the input echoed through untouched.
	if bytes.HasPrefix(prefix, messagesPrefix(t, in1, 2)) {
		t.Fatal("turn 1 did not actually rewrite message 2, so the prefix claim proves nothing")
	}
}

// The negative control for the test above. Without replay, the naive
// frozen-floor fix does exactly what the bead predicts: turn 2 re-sends the
// original, headroom leaves it alone, and the cached prefix no longer matches.
//
// If this test ever passes, the headline test above has stopped measuring
// anything.
func TestWithoutReplayTheSecondTurnRewritesTheCachedPrefix(t *testing.T) {
	turn1, turn2, _, in2 := twoTurns(t, false)

	prefix := messagesPrefix(t, turn1, 2)
	if bytes.HasPrefix(turn2, prefix) {
		t.Fatal("expected the naive fix to bust the cached prefix, but turn 2 matched turn 1")
	}
	// And it busts it by reverting to the original bytes, which is the
	// specific failure the bead describes.
	if !bytes.HasPrefix(turn2, messagesPrefix(t, in2, 2)) {
		t.Fatal("turn 2 neither replayed nor passed through: the fixture no longer models the naive fix")
	}
}

// Replay must reach BELOW the frozen floor. That is the part that looks unsafe
// and is not: it reproduces what the provider already cached.
func TestReplayRewritesBelowTheFrozenFloor(t *testing.T) {
	opts := liveOptions(t)
	opts.Replay = cachestab.NewReplayState(8).Begin("s", nil)
	log := compressibleLog()

	opts.FrozenCount = 0
	if res := Dispatch(replayBody(log, 0), opts); !res.Applied {
		t.Fatalf("turn 1 must compress; got %q", res.Reason)
	}

	// A floor of 5 freezes the whole conversation: the fresh pass finds no
	// live zone at all, so anything rewritten came from replay.
	in2 := replayBody(log, 1)
	opts.FrozenCount = 5
	res2 := Dispatch(in2, opts)
	if !res2.Applied {
		t.Fatalf("replay must apply with the whole conversation frozen; got %q", res2.Reason)
	}
	for _, b := range res2.Blocks {
		if b.Action != "replayed" {
			t.Fatalf("with floor 5 every outcome must come from replay, got %q at message %d", b.Action, b.MessageIndex)
		}
		if b.MessageIndex >= res2.FrozenCount {
			t.Fatalf("replayed block at message %d is not below the frozen floor %d", b.MessageIndex, res2.FrozenCount)
		}
	}
	if gjson.GetBytes(res2.Body, "messages.2.content.0.content").Str == log {
		t.Fatal("message 2 still carries the original text: replay did not fire")
	}
}

// A replayed block's outcome must name its branch, so a fixture that drifts
// onto the fresh-compression path fails loudly instead of passing quietly.
func TestReplayedBlockOutcomeNamesItsBranchAndItsKey(t *testing.T) {
	opts := liveOptions(t)
	opts.Replay = cachestab.NewReplayState(8).Begin("s", nil)
	log := compressibleLog()

	opts.FrozenCount = 0
	res1 := Dispatch(replayBody(log, 0), opts)
	if !res1.Applied {
		t.Fatalf("turn 1 must compress; got %q", res1.Reason)
	}

	opts.FrozenCount = 3
	res2 := Dispatch(replayBody(log, 1), opts)

	var replayed *BlockOutcome
	for i := range res2.Blocks {
		if res2.Blocks[i].Action == "replayed" {
			replayed = &res2.Blocks[i]
		}
	}
	if replayed == nil {
		t.Fatalf("no outcome with Action %q; got %+v", "replayed", res2.Blocks)
	}
	if want := ccr.ComputeKey([]byte(log)); replayed.CacheKey != want {
		t.Errorf("replayed CacheKey = %q, want the original's ComputeKey %q", replayed.CacheKey, want)
	}
	if replayed.MessageIndex != 2 {
		t.Errorf("replayed MessageIndex = %d, want 2", replayed.MessageIndex)
	}
	if replayed.TokensAfter >= replayed.TokensBefore {
		t.Errorf("replayed block reports %d -> %d tokens, which is not a saving",
			replayed.TokensBefore, replayed.TokensAfter)
	}
}

// countingStore records what reached the backing store, so the TTL-refresh
// write can be asserted as an effect rather than read off the source.
type countingStore struct {
	ccr.Store
	puts map[string]int
}

func (c *countingStore) Put(hash, payload string) {
	c.puts[hash]++
	c.Store.Put(hash, payload)
}

// The replayed text carries a <<ccr:HASH>> marker that stays on the wire for
// the rest of the session, while ccr.DefaultTTL is five minutes. Replay must
// therefore re-store the original on every turn, or the marker outlives the
// entry it dereferences.
func TestReplayRefreshesTheStoreEntryBehindTheMarkerOnTheWire(t *testing.T) {
	opts := liveOptions(t)
	store := &countingStore{Store: opts.Store, puts: map[string]int{}}
	opts.Store = store
	opts.Replay = cachestab.NewReplayState(8).Begin("s", nil)
	log := compressibleLog()
	key := ccr.ComputeKey([]byte(log))

	opts.FrozenCount = 0
	if res := Dispatch(replayBody(log, 0), opts); !res.Applied {
		t.Fatalf("turn 1 must compress; got %q", res.Reason)
	}
	if store.puts[key] != 1 {
		t.Fatalf("turn 1 stored the original %d times, want 1", store.puts[key])
	}

	opts.FrozenCount = 3
	if res := Dispatch(replayBody(log, 1), opts); !res.Applied {
		t.Fatalf("turn 2 must replay; got %q", res.Reason)
	}
	if store.puts[key] != 2 {
		t.Fatalf("turn 2 stored the original %d times in total, want 2: a replayed marker "+
			"whose entry is never refreshed dangles once the TTL expires", store.puts[key])
	}
}

// Every canonical marker the replayed body carries must still resolve. This is
// the same contract the fresh path already keeps, asserted on the turn where
// the block is no longer being compressed.
func TestReplayedBodyMarkersResolve(t *testing.T) {
	opts := liveOptions(t)
	opts.Replay = cachestab.NewReplayState(8).Begin("s", nil)
	log := compressibleLog()

	opts.FrozenCount = 0
	if res := Dispatch(replayBody(log, 0), opts); !res.Applied {
		t.Fatalf("turn 1 must compress; got %q", res.Reason)
	}
	opts.FrozenCount = 3
	res2 := Dispatch(replayBody(log, 1), opts)
	if !res2.Applied {
		t.Fatalf("turn 2 must replay; got %q", res2.Reason)
	}

	hashes := markerHashes(string(res2.Body))
	if len(hashes) == 0 {
		t.Fatal("the replayed body carries no marker at all, so this test proves nothing")
	}
	for _, h := range hashes {
		if _, ok := opts.Store.Get(h); !ok {
			t.Errorf("marker <<ccr:%s>> on the wire does not resolve in the store", h)
		}
	}
}

// markerHashes pulls every 24-hex-char CCR hash out of a body, in either
// marker format. Enumerating what the wire actually carries beats checking one
// key scheme: this repo has shipped a test that missed an orphan because it
// asserted on the wrong hash family.
func markerHashes(body string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(body[i:], "<<ccr:")
		if j < 0 {
			return out
		}
		i += j + len("<<ccr:")
		if len(body[i:]) < 24 {
			return out
		}
		h := body[i : i+24]
		if strings.Trim(h, "0123456789abcdef") == "" {
			out = append(out, h)
		}
	}
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return "..." + string(b[len(b)-n:])
}

// A block that is BOTH replayed and inside the live zone must be replayed, not
// compressed a second time. This is the client-retry shape: the same request
// arrives again after a 5xx, so the newest user message is one headroom
// already rewrote.
//
// Without the claimed-slot skip the fresh pass re-runs the compressor over it.
// Today that happens to produce the same bytes and applyReplacements drops the
// overlap, so the body survives by accident — but the store is written twice
// and the precedence between the two passes is emergent rather than stated.
func TestAReplayedBlockInTheLiveZoneIsNotCompressedTwice(t *testing.T) {
	opts := liveOptions(t)
	store := &countingStore{Store: opts.Store, puts: map[string]int{}}
	opts.Store = store
	opts.Replay = cachestab.NewReplayState(8).Begin("s", nil)
	log := compressibleLog()
	key := ccr.ComputeKey([]byte(log))

	opts.FrozenCount = 0
	if res := Dispatch(replayBody(log, 0), opts); !res.Applied {
		t.Fatalf("turn 1 must compress; got %q", res.Reason)
	}

	// The retry: identical body, identical floor, so message 2 is both in
	// the replay map and the latest user message.
	res2 := Dispatch(replayBody(log, 0), opts)
	if !res2.Applied {
		t.Fatalf("the retry must still be rewritten; got %q", res2.Reason)
	}
	for _, b := range res2.Blocks {
		if b.Action == "compressed" {
			t.Errorf("message %d block %d was compressed again instead of replayed",
				b.MessageIndex, b.Index)
		}
	}
	if store.puts[key] != 2 {
		t.Errorf("the original was stored %d times over two turns, want 2: the fresh pass "+
			"re-ran the compressor over a block replay had already claimed", store.puts[key])
	}
}

// The splice guard. A hot-zone slot carries no text and no byte offsets, so
// splicing one would write at offset 0 and corrupt the body — the same hazard
// stringSlot's Index <= 0 rule exists for.
//
// The map cannot hold an empty-text entry today, so this seeds one directly.
// The guard is what keeps that true if the recording identity ever changes.
func TestReplayNeverSplicesASlotWithNoByteRange(t *testing.T) {
	opts := liveOptions(t)
	handle := cachestab.NewReplayState(8).Begin("s", nil)
	handle.Record(ccr.ComputeKey([]byte("")), "boom")
	opts.Replay = handle
	opts.FrozenCount = 5

	in := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"tool_use","id":"t1","name":"bash","input":{}}]}` +
		`]}`)
	res := Dispatch(in, opts)
	if res.Applied {
		t.Fatalf("a hot-zone-only body was rewritten: %s", res.Body)
	}
	if !bytes.Equal(res.Body, in) {
		t.Fatalf("body mutated:\n got %s\nwant %s", res.Body, in)
	}
	if bytes.Contains(res.Body, []byte("boom")) {
		t.Fatal("a zero-length slot was spliced at offset 0 and corrupted the body")
	}
}

// Not every compressor appends an inline "hash=" marker — log_template does
// not, and it is the strategy that fired on real Claude Code traffic. Replay
// must refresh only the entries the replayed text actually names, or it writes
// a second full copy of every such original under a key no marker points at:
// an orphan, and the store grows at twice the rate it should.
func TestReplayRefreshesOnlyTheEntriesTheWireNames(t *testing.T) {
	original := strings.Repeat("a line of tool output\n", 60)
	hash := ccr.ComputeKey([]byte(original))
	// A replayed text carrying ONLY the canonical marker: no hash= anywhere.
	compressed := "compressed form\n" + ccr.MarkerFor(hash)
	if strings.Contains(compressed, "hash=") {
		t.Fatal("the fixture carries an inline marker; it cannot test the orphan case")
	}

	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"` + strings.ReplaceAll(original, "\n", "\\n") + `"}]},` +
		`{"role":"user","content":[{"type":"text","text":"newer"}]}]}`)

	state := cachestab.NewReplayState(4)
	h := state.Begin("claude:test", body)
	h.Record(hash, compressed)

	store := newMapStore()
	res := Dispatch(body, Options{
		Router:      router.NewDefault(),
		Store:       store,
		Tokenizer:   tokenizer.GetTokenizer(DefaultModel),
		FrozenCount: 2,
		Replay:      h,
	})
	if !res.Applied {
		t.Fatal("the block was not replayed; this test would prove nothing")
	}

	// Exactly the canonical entry, and nothing else. The MD5 key of the same
	// original is the orphan an unconditional refresh would write.
	if store.Len() != 1 {
		t.Errorf("the store holds %d entries for one canonical marker", store.Len())
	}
	if _, ok := store.Get(ccr.ComputeKeyMD5([]byte(original))); ok {
		t.Error("replay wrote an MD5-keyed entry that no marker on the wire names")
	}
	if got, ok := store.Get(hash); !ok || got != original {
		t.Error("the canonical entry was not refreshed")
	}
}
