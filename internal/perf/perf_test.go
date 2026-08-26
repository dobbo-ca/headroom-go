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
	if none.TokensAvoidedShare() > 0 {
		t.Errorf("a no-op turn claimed %v of cost avoided", none.TokensAvoidedShare())
	}
	if saved.TokensAvoidedShare() <= 0 {
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
	if r.TokensAvoidedShare() > 0 {
		t.Errorf("a price was estimated from an unsound join: %v", r.TokensAvoidedShare())
	}
	out := Format(r)
	if !strings.Contains(out, "No cache verdict") {
		t.Errorf("the report did not withhold its verdict:\n%s", out)
	}
	// The measured half must still be reported: the bytes are real. Match
	// the whole labelled field — "10.0%" alone is a substring of "100.0%",
	// so a report that invented a perfect saving would still pass.
	if !strings.Contains(out, "bytes saved       10.0%") {
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

// TokensAvoidedShare is a pure token ratio; the price multipliers cancel
// out algebraically. Three reports with the same TokensRemoved and same
// InputTokens(Headroom) but wildly different price mixes must produce
// identical outputs, proving the expression is price-free.
func TestTokensAvoidedShareIgnoresThePriceMix(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("price-test")
	const removed = 50000
	const total = 1000000
	const after = 200000

	// Three price mixes that differ by 20x in InputUnits but have the same
	// TokensRemoved and InputTokens(Headroom).
	allRead := Build(
		[]ledger.Entry{{Session: digest, TokensBefore: removed + after, TokensAfter: after, BytesIn: 100, BytesOut: 90}},
		[]Session{{Digest: digest, Turns: []Usage{usage(0, total, 0, 0)}}})
	allFresh := Build(
		[]ledger.Entry{{Session: digest, TokensBefore: removed + after, TokensAfter: after, BytesIn: 100, BytesOut: 90}},
		[]Session{{Digest: digest, Turns: []Usage{usage(total, 0, 0, 0)}}})
	all1hWrite := Build(
		[]ledger.Entry{{Session: digest, TokensBefore: removed + after, TokensAfter: after, BytesIn: 100, BytesOut: 90}},
		[]Session{{Digest: digest, Turns: []Usage{usage(0, 0, 0, total)}}})

	// All three must produce the same share.
	got1 := allRead.TokensAvoidedShare()
	got2 := allFresh.TokensAvoidedShare()
	got3 := all1hWrite.TokensAvoidedShare()
	want := float64(removed) / float64(removed+total)

	if got1 != got2 || got2 != got3 {
		t.Errorf("TokensAvoidedShare varied across price mixes: allRead=%v, allFresh=%v, all1hWrite=%v", got1, got2, got3)
	}
	if got1 != want {
		t.Errorf("TokensAvoidedShare = %v, want %v (pure token share)", got1, want)
	}

	// Prove the fixture really varies the price: InputUnits must differ by at least 10x.
	u1, u2, u3 := InputUnits(allRead.Headroom), InputUnits(allFresh.Headroom), InputUnits(all1hWrite.Headroom)
	if u3/u1 < 10 {
		t.Errorf("InputUnits did not vary enough: read=%v, fresh=%v, 1h=%v; fixture is too similar", u1, u2, u3)
	}
}

// CostRange brackets the cost share between the read multiplier (low end)
// and the 1h write multiplier (high end). The bracket must span, ordering
// must hold, and when !JoinSound both ends must be -1.
func TestCostRangeSpansReadAndWritePricing(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("cost-range-test")
	// A read-dominated mix: most of the traffic would have been cached reads.
	r := Build(
		[]ledger.Entry{{Session: digest, TokensBefore: 250, TokensAfter: 200, BytesIn: 100, BytesOut: 90}},
		[]Session{{Digest: digest, Turns: []Usage{usage(19000, 933000, 0, 48000)}}})

	low, high := r.CostRange()
	// Hand-computed: T=50, total=1M, at(0.1) = 50*0.1/(1M+5) ~= 0.0049995, at(2.0) = 50*2/(1M+100) ~= 0.0999001
	wantLow := float64(50*PriceCacheRead) / (InputUnits(r.Headroom) + float64(50*PriceCacheRead))
	wantHigh := float64(50*PriceCacheWrite1h) / (InputUnits(r.Headroom) + float64(50*PriceCacheWrite1h))

	if low != wantLow || high != wantHigh {
		t.Errorf("CostRange = (%v, %v), want (%v, %v)", low, high, wantLow, wantHigh)
	}
	if high/low <= 5 {
		t.Errorf("CostRange span too narrow for a read-dominated mix: low=%v, high=%v, ratio=%v", low, high, high/low)
	}

	// When !JoinSound, both ends must be -1.
	unsound := Build(
		[]ledger.Entry{{Session: digest, TokensBefore: 19176, TokensAfter: 0, BytesIn: 100, BytesOut: 90}},
		[]Session{{Digest: digest, Turns: []Usage{usage(50, 0, 0, 0)}}})
	lowU, highU := unsound.CostRange()
	if lowU != -1 || highU != -1 {
		t.Errorf("unsound join: CostRange = (%v, %v), want (-1, -1)", lowU, highU)
	}
}

// A metered report (more than half the turns are NOT subscription) must
// headline "input tokens never sent" and demote bytes to a labelled line
// below it. The old "what a user actually sees" must never appear.
func TestMeteredReportHeadlinesTokensAndDemotesBytes(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("metered-test")
	entries := []ledger.Entry{
		{Session: digest, Mode: "payg", TokensBefore: 100, TokensAfter: 50, BytesIn: 1000, BytesOut: 900},
	}
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(100, 900, 0, 0)}}}
	r := Build(entries, sessions)

	out := Format(r)
	idxTokens := strings.Index(out, "input tokens never sent")
	idxBytes := strings.Index(out, "bytes saved")

	if idxTokens < 0 {
		t.Errorf("metered report missing 'input tokens never sent' headline:\n%s", out)
	}
	if idxBytes < 0 {
		t.Errorf("metered report missing 'bytes saved' line:\n%s", out)
	}
	if idxTokens > idxBytes {
		t.Errorf("tokens headline must appear before bytes line: tokens at %d, bytes at %d\n%s", idxTokens, idxBytes, out)
	}
	if strings.Contains(out, "what a user actually sees") {
		t.Errorf("old label must not appear:\n%s", out)
	}
	if strings.Contains(out, "context-window headroom") {
		t.Errorf("subscription label leaked into metered report:\n%s", out)
	}
}

// A subscription report (more than half the turns are subscription mode)
// must headline "context-window headroom" and mention "no per-token bill".
// The metered label must not appear. PAIRED CONTROL: two reports differing
// only in Mode must produce complementary outputs, and their
// TokensAvoidedShare must be equal (proves only the framing moved).
func TestSubscriptionReportHeadlinesWindowHeadroom(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("subscription-test")
	base := []ledger.Entry{{Session: digest, TokensBefore: 100, TokensAfter: 50, BytesIn: 1000, BytesOut: 900}}
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(100, 900, 0, 0)}}}

	// Build two reports differing only in Mode.
	sub := base[0]
	sub.Mode = "subscription"
	payg := base[0]
	payg.Mode = "payg"

	rSub := Build([]ledger.Entry{sub}, sessions)
	rPayg := Build([]ledger.Entry{payg}, sessions)

	outSub := Format(rSub)
	outPayg := Format(rPayg)

	// Subscription output must contain its label and explanation.
	if !strings.Contains(outSub, "context-window headroom") {
		t.Errorf("subscription report missing 'context-window headroom':\n%s", outSub)
	}
	if !strings.Contains(outSub, "no per-token bill") {
		t.Errorf("subscription report missing 'no per-token bill' explanation:\n%s", outSub)
	}
	if strings.Contains(outSub, "what never reached the meter") {
		t.Errorf("metered label leaked into subscription report:\n%s", outSub)
	}

	// PAYG output must be the complement.
	if strings.Contains(outPayg, "context-window headroom") {
		t.Errorf("subscription label appeared in payg report:\n%s", outPayg)
	}
	if strings.Contains(outPayg, "no per-token bill") {
		t.Errorf("subscription explanation appeared in payg report:\n%s", outPayg)
	}

	// The arithmetic must not have changed: only the framing.
	if rSub.TokensAvoidedShare() != rPayg.TokensAvoidedShare() {
		t.Errorf("TokensAvoidedShare differed: sub=%v, payg=%v; only the label should change", rSub.TokensAvoidedShare(), rPayg.TokensAvoidedShare())
	}
}

// An entry with no Mode field (ledger written before v0.2) must count as
// metered. Unknown is the safest default: a wrong "no bill" claim
// contradicts an invoice; a wrong cost claim only carries the assumption
// it already states.
func TestUnknownModeIsTreatedAsMetered(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("unknown-mode-test")
	entries := []ledger.Entry{
		{Session: digest, Mode: "", TokensBefore: 100, TokensAfter: 50, BytesIn: 1000, BytesOut: 900},
	}
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(100, 900, 0, 0)}}}
	r := Build(entries, sessions)

	if !r.Metered() {
		t.Errorf("entry with Mode='' must count as metered, got Metered()=%v", r.Metered())
	}

	// Both unknown and payg must produce the metered headline.
	payg := entries[0]
	payg.Mode = "payg"
	rPayg := Build([]ledger.Entry{payg}, sessions)
	outUnknown := Format(r)
	outPayg := Format(rPayg)
	if !strings.Contains(outUnknown, "input tokens never sent") {
		t.Errorf("unknown-mode output missing metered headline:\n%s", outUnknown)
	}
	if !strings.Contains(outPayg, "input tokens never sent") {
		t.Errorf("payg output missing metered headline:\n%s", outPayg)
	}
	if strings.Contains(outUnknown, "context-window headroom") || strings.Contains(outPayg, "context-window headroom") {
		t.Errorf("subscription label leaked into metered report")
	}
}

// Metered() must require more than half the turns to be subscription. A
// tie is metered (the safest default). Table: 2 sub / 3 payg => metered;
// 3 sub / 2 payg => subscription; 2 sub / 2 payg => metered.
func TestSubscriptionNeedsMoreThanHalfTheTurns(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("majority-test")
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(100, 900, 0, 0)}}}

	cases := []struct {
		sub, payg int
		want      bool   // true = metered
		label     string // which headline should appear
	}{
		{2, 3, true, "input tokens never sent"},  // minority subscription
		{3, 2, false, "context-window headroom"}, // majority subscription
		{2, 2, true, "input tokens never sent"},  // tie is metered
	}

	for _, tc := range cases {
		var entries []ledger.Entry
		for i := 0; i < tc.sub; i++ {
			entries = append(entries, ledger.Entry{Session: digest, Mode: "subscription", TokensBefore: 100, TokensAfter: 50, BytesIn: 100, BytesOut: 90})
		}
		for i := 0; i < tc.payg; i++ {
			entries = append(entries, ledger.Entry{Session: digest, Mode: "payg", TokensBefore: 100, TokensAfter: 50, BytesIn: 100, BytesOut: 90})
		}
		r := Build(entries, sessions)
		if r.Metered() != tc.want {
			t.Errorf("%d sub / %d payg: Metered()=%v, want %v", tc.sub, tc.payg, r.Metered(), tc.want)
		}
		out := Format(r)
		if !strings.Contains(out, tc.label) {
			t.Errorf("%d sub / %d payg: output missing '%s':\n%s", tc.sub, tc.payg, tc.label, out)
		}
	}
}

// When !JoinSound(), the share and cost range must be withheld in BOTH
// modes. Bytes saved is still present (it is ledger-only, not combined).
// This extends the existing TestJoinSoundnessGuardsEveryCombinedNumber.
func TestJoinUnsoundWithholdsTheShareInBothModes(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("unsound-both-modes")
	entries := []ledger.Entry{{Session: digest, TokensBefore: 19176, TokensAfter: 0, BytesIn: 1000, BytesOut: 900}}
	sessions := []Session{{Digest: digest, Turns: []Usage{usage(50, 0, 0, 0)}}}

	// Run twice, once per mode.
	for _, mode := range []string{"subscription", "payg"} {
		e := entries[0]
		e.Mode = mode
		r := Build([]ledger.Entry{e}, sessions)
		out := Format(r)

		// The verdict sentence must be present.
		if !strings.Contains(out, "No cache verdict") {
			t.Errorf("mode=%s: missing 'No cache verdict':\n%s", mode, out)
		}
		// The shares must be absent.
		if strings.Contains(out, "context-window headroom") {
			t.Errorf("mode=%s: subscription headline leaked onto withholding path:\n%s", mode, out)
		}
		if strings.Contains(out, "input tokens never sent") {
			t.Errorf("mode=%s: metered headline leaked onto withholding path:\n%s", mode, out)
		}
		if strings.Contains(out, "of your input bill") {
			t.Errorf("mode=%s: cost bracket leaked onto withholding path:\n%s", mode, out)
		}
		// Bytes saved is still present: it is ledger-only.
		if !strings.Contains(out, "bytes saved       10.0%") {
			t.Errorf("mode=%s: bytes saved withheld or wrong:\n%s", mode, out)
		}
	}
}

// When MatchedSessions == 0, the report prints the absolute count but no
// share (there is no denominator). The arrow "<-" must not appear anywhere.
func TestNoJoinPrintsTheAbsoluteCountWithNoArrow(t *testing.T) {
	digest := cachestab.ClaudeSessionDigest("no-join-test")
	entries := []ledger.Entry{{Session: digest, TokensBefore: 100, TokensAfter: 40, BytesIn: 1000, BytesOut: 900}}
	r := Build(entries, nil)

	if r.MatchedSessions != 0 {
		t.Fatalf("fixture must have MatchedSessions=0, got %d", r.MatchedSessions)
	}

	out := Format(r)
	// The absolute count must be present.
	if !strings.Contains(out, "tokens removed") {
		t.Errorf("absolute count missing:\n%s", out)
	}
	// The arrow must not appear.
	if strings.Contains(out, "<-") {
		t.Errorf("arrow appeared with no join:\n%s", out)
	}
	// No NaN or +Inf from a zero denominator.
	if strings.Contains(out, "NaN") || strings.Contains(out, "+Inf") {
		t.Errorf("divide-by-zero leaked:\n%s", out)
	}
}
