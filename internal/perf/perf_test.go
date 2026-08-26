package perf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ledger"
)

func usage(input, read, w5m, w1h int) Usage {
	var u Usage
	u.InputTokens = input
	u.CacheReadTokens = read
	u.CacheCreation.Ephemeral5m = w5m
	u.CacheCreation.Ephemeral1h = w1h
	u.CacheCreationTokens = w5m + w1h
	return u
}

// The whole verdict rests on these four multipliers. A 1-hour write costs 2x,
// not the 1.25x a 5-minute write costs, and Claude Code uses the 1-hour TTL —
// so merging them understates what the cache cost by 60%.
func TestInputUnitsPricesEachTTLSeparately(t *testing.T) {
	got := InputUnits(usage(1000, 1000, 1000, 1000))
	want := 1000*1.0 + 1000*0.1 + 1000*1.25 + 1000*2.0
	if got != want {
		t.Errorf("InputUnits = %v, want %v", got, want)
	}
	// The two writes must not price the same.
	if InputUnits(usage(0, 0, 1000, 0)) == InputUnits(usage(0, 0, 0, 1000)) {
		t.Error("a 1-hour cache write must cost more than a 5-minute one")
	}
	// A cached read must be the cheapest thing on the bill.
	if InputUnits(usage(0, 1000, 0, 0)) >= InputUnits(usage(1000, 0, 0, 0)) {
		t.Error("a cached read must cost less than fresh input")
	}
}

// A record with no per-TTL split must still be priced, and at the cheaper of
// the two rates so the report never flatters headroom.
func TestInputUnitsPricesAnUnsplitWriteAtTheCheaperRate(t *testing.T) {
	var u Usage
	u.CacheCreationTokens = 1000
	if got, want := InputUnits(u), 1000*PriceCacheWrite5m; got != want {
		t.Errorf("InputUnits = %v, want %v", got, want)
	}
}

func TestCacheReadShare(t *testing.T) {
	if got := CacheReadShare(usage(100, 900, 0, 0)); got != 0.9 {
		t.Errorf("CacheReadShare = %v, want 0.9", got)
	}
	if got := CacheReadShare(Usage{}); got != -1 {
		t.Errorf("CacheReadShare of nothing = %v, want -1", got)
	}
}

// Build must route a transcript to the headroom side only when its digest
// appears in the ledger, and to the baseline otherwise. Getting this backwards
// would compare headroom against itself and always report "no change".
func TestBuildJoinsOnTheSessionDigest(t *testing.T) {
	const proxied = "11111111-1111-1111-1111-111111111111"
	const unproxied = "22222222-2222-2222-2222-222222222222"

	entries := []ledger.Entry{{
		TS: time.Now().Format(time.RFC3339), Session: cachestab.ClaudeSessionDigest(proxied),
		BytesIn: 1000, BytesOut: 800, TokensBefore: 100, TokensAfter: 40,
		Reason: "ok", Strategies: []string{"log_offload"}, Replayed: 2, Drift: []string{"tools"},
	}}
	sessions := []Session{
		{ID: proxied, Digest: cachestab.ClaudeSessionDigest(proxied), Turns: []Usage{usage(10, 900, 0, 90)}},
		{ID: unproxied, Digest: cachestab.ClaudeSessionDigest(unproxied), Turns: []Usage{usage(500, 500, 0, 0)}},
	}

	r := Build(entries, sessions)
	if r.MatchedSessions != 1 || r.BaselineSessions != 1 {
		t.Fatalf("matched %d, baseline %d; want 1 and 1", r.MatchedSessions, r.BaselineSessions)
	}
	if r.Headroom.CacheReadTokens != 900 {
		t.Errorf("the proxied session's usage did not land on the headroom side: %+v", r.Headroom)
	}
	if r.Baseline.CacheReadTokens != 500 {
		t.Errorf("the unproxied session's usage did not land on the baseline side: %+v", r.Baseline)
	}
	if r.Turns != 1 || r.CompressedTurns != 1 || r.Replayed != 2 {
		t.Errorf("ledger side = %+v", r)
	}
	if r.DriftTurns != 1 || r.DriftDims["tools"] != 1 {
		t.Errorf("drift not counted: %+v", r.DriftDims)
	}
	if got := r.WholeBodySaving(); got != 0.2 {
		t.Errorf("WholeBodySaving = %v, want 0.2", got)
	}
	if r.TokensRemoved() != 60 {
		t.Errorf("TokensRemoved = %d, want 60", r.TokensRemoved())
	}
}

// CONTROL. Before believing any report, prove the measurement can see the
// thing it is looking for. Two ledgers differing only in bytes_out must
// produce two different savings, and a no-op ledger must report zero.
func TestReportSeesASavingAndSeesItsAbsence(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("aaaaaaaa-0000-0000-0000-000000000000")
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(100, 9000, 0, 900)}}}

	saved := Build([]ledger.Entry{{
		Session: digest, BytesIn: 1000, BytesOut: 500, TokensBefore: 200, TokensAfter: 100,
	}}, sessions)
	none := Build([]ledger.Entry{{
		Session: digest, BytesIn: 1000, BytesOut: 1000, TokensBefore: 200, TokensAfter: 200,
	}}, sessions)

	if saved.WholeBodySaving() != 0.5 {
		t.Errorf("a real 50%% saving reported as %v", saved.WholeBodySaving())
	}
	if none.WholeBodySaving() != 0 {
		t.Errorf("a no-op turn reported a saving of %v", none.WholeBodySaving())
	}
	if none.InputCostAvoided() > 0 {
		t.Errorf("a no-op turn claimed %v of cost avoided", none.InputCostAvoided())
	}
	if saved.InputCostAvoided() <= 0 {
		t.Error("a real saving produced no cost estimate; the measurement cannot see what it is for")
	}
}

// The price estimate and the cache verdict must be withheld when the two
// sources cannot be describing the same traffic. This is what stops a test
// sink, or a half-written transcript, from producing a confident wrong number.
func TestJoinSoundnessGuardsEveryCombinedNumber(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("bbbbbbbb-0000-0000-0000-000000000000")
	// 19,176 tokens removed against 50 tokens billed: exactly the shape a
	// run against a fake upstream produces.
	r := Build([]ledger.Entry{{
		Session: digest, BytesIn: 1000, BytesOut: 900, TokensBefore: 19176, TokensAfter: 0,
	}}, []Session{{Digest: digest, Turns: []Usage{usage(50, 0, 0, 0)}}})

	if r.JoinSound() {
		t.Fatal("a 19176-token saving against 50 billed tokens must not count as a sound join")
	}
	if r.InputCostAvoided() > 0 {
		t.Errorf("a price was estimated from an unsound join: %v", r.InputCostAvoided())
	}
	out := Format(r)
	if !strings.Contains(out, "No cache verdict") {
		t.Errorf("the report did not withhold its verdict:\n%s", out)
	}
	// The measured half must still be reported: the bytes are real. Match
	// the whole labelled field — "10.0%" alone is a substring of "100.0%",
	// so a report that invented a perfect saving would still pass.
	if !strings.Contains(out, "whole-body saving 10.0%") {
		t.Errorf("the measured byte saving was withheld or wrong:\n%s", out)
	}
}

func TestSinceDropsOlderEntries(t *testing.T) {
	now := time.Now()
	entries := []ledger.Entry{
		{Session: "a", TS: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{Session: "b", TS: now.Add(-1 * time.Hour).Format(time.RFC3339)},
		{Session: "c", TS: "not a timestamp"},
	}
	got := Since(entries, now.Add(-24*time.Hour))
	if len(got) != 2 || got[0].Session != "b" || got[1].Session != "c" {
		t.Errorf("Since kept %+v; want the recent entry and the untimestamped one", got)
	}
}

// LoadSessions must read subagent transcripts too. They are 79% of real agent
// traffic, so a report that missed them would describe a fifth of the day.
func TestLoadSessionsWalksSubagentTranscripts(t *testing.T) {
	root := t.TempDir()
	top := filepath.Join(root, "project")
	sub := filepath.Join(top, "sess", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_read_input_tokens":10,` +
		`"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5,"ephemeral_5m_input_tokens":0}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(top, "aaa.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-bbb.jsonl"), []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file with no usage records at all must not become a session.
	if err := os.WriteFile(filepath.Join(top, "empty.jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSessions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d sessions, want the top-level one and the subagent one", len(got))
	}
	turns := 0
	for _, s := range got {
		turns += len(s.Turns)
		if s.Digest != cachestab.ClaudeSessionDigest(s.ID) {
			t.Errorf("session %q digested to %q", s.ID, s.Digest)
		}
	}
	if turns != 3 {
		t.Errorf("loaded %d turns, want 3", turns)
	}
}

// An empty ledger must say so rather than print a report full of zeroes that
// reads like a measurement.
func TestFormatSaysNothingHasBeenRecorded(t *testing.T) {
	out := Format(Build(nil, nil))
	if !strings.Contains(out, "No turns recorded") {
		t.Errorf("an empty ledger rendered as:\n%s", out)
	}
}
