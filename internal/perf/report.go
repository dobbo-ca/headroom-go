package perf

import (
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/policy"
)

// Anthropic's prompt-cache price multipliers, relative to the base input
// price. A cached read is nearly free; a write is dearer than sending the
// tokens fresh, and a 1-hour write is dearer still. Claude Code uses the
// 1-hour TTL, so a report that merged the two would understate its cost.
const (
	PriceFreshInput   = 1.0
	PriceCacheRead    = 0.1
	PriceCacheWrite5m = 1.25
	PriceCacheWrite1h = 2.0
)

// Report is one answer to "did this help or hurt?".
type Report struct {
	LedgerPath string
	Since      time.Time
	Until      time.Time

	// What headroom did, straight from its own ledger.
	Turns           int
	CompressedTurns int
	Sessions        int
	BytesIn         int64
	BytesOut        int64
	TokensBefore    int
	TokensAfter     int
	Strategies      map[string]int
	Replayed        int
	DriftTurns      int
	DriftDims       map[string]int
	Reasons         map[string]int
	Modes           map[string]int

	// What the cache did, from Claude Code's own usage records.
	MatchedSessions  int
	Headroom         Usage
	BaselineSessions int
	Baseline         Usage
}

// InputUnits prices one usage record in multiples of the base input price.
// Output tokens are excluded: headroom cannot change them, so including them
// would dilute exactly the ratio the report exists to show.
func InputUnits(u Usage) float64 {
	// A record from an older client may report cache_creation_input_tokens
	// without the per-TTL split. Price that remainder at the 5-minute rate,
	// which is the cheaper of the two, so the report never flatters headroom
	// by overstating what the cache cost without it.
	split := u.CacheCreation.Ephemeral5m + u.CacheCreation.Ephemeral1h
	rest := u.CacheCreationTokens - split
	if rest < 0 {
		rest = 0
	}
	return float64(u.InputTokens)*PriceFreshInput +
		float64(u.CacheReadTokens)*PriceCacheRead +
		float64(u.CacheCreation.Ephemeral5m)*PriceCacheWrite5m +
		float64(u.CacheCreation.Ephemeral1h)*PriceCacheWrite1h +
		float64(rest)*PriceCacheWrite5m
}

// InputTokens is every input token billed in any form.
func InputTokens(u Usage) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// CacheReadShare is the fraction of input tokens served from the cached
// prefix. It is the single number that says whether the prefix held: bytes
// saved with a busted cache is a loss, and this is what catches that.
// Returns -1 when there is nothing to divide.
func CacheReadShare(u Usage) float64 {
	total := InputTokens(u)
	if total == 0 {
		return -1
	}
	return float64(u.CacheReadTokens) / float64(total)
}

// WholeBodySaving is the fraction of the bytes headroom removed from what the
// client tried to send. Bytes, not tokens and not money. It is the widest
// number this report can print and the one to trust least; it is kept because
// bytes-out is the ground truth of what was sent. Returns -1 when nothing was sent.
func (r Report) WholeBodySaving() float64 {
	if r.BytesIn == 0 {
		return -1
	}
	return float64(r.BytesIn-r.BytesOut) / float64(r.BytesIn)
}

// TokensRemoved is the token count headroom took out of the blocks it touched.
func (r Report) TokensRemoved() int { return r.TokensBefore - r.TokensAfter }

// TokensAvoidedShare is the share of input tokens headroom kept out of the
// requests these sessions sent. It is deliberately price-free: it is the one
// thing money and window space have in common. The old InputCostAvoided
// computed exactly this expression and called it cost — the price multipliers
// cancel, see TestTokensAvoidedShareIgnoresThePriceMix.
func (r Report) TokensAvoidedShare() float64 {
	if !r.JoinSound() || r.TokensRemoved() <= 0 {
		return -1
	}
	return float64(r.TokensRemoved()) / float64(r.TokensRemoved()+InputTokens(r.Headroom))
}

// CostRange brackets the share of input cost headroom avoided on a metered
// plan. The bracket is the whole point: unlike TokensAvoidedShare the
// multipliers do not cancel here, and which end applies is not observable.
//
// Low end: the removed tokens would have been cached reads at 0.1x — true for
// a block headroom replays turn after turn. High end: a 1-hour cache write at
// 2.0x — true for the turn a block is first removed, since the fresh pass only
// ever touches the LATEST user message, content the provider has not seen.
// The ledger records a replayed BLOCK count, not replayed TOKENS, so the split
// is not computable and the report gives the range instead of a point.
func (r Report) CostRange() (low, high float64) {
	units := InputUnits(r.Headroom)
	if !r.JoinSound() || r.TokensRemoved() <= 0 || units <= 0 {
		return -1, -1
	}
	at := func(p float64) float64 { a := float64(r.TokensRemoved()) * p; return a / (units + a) }
	return at(PriceCacheRead), at(PriceCacheWrite1h)
}

// Metered decides which headline this report leads with.
//
// Subscription is the only mode where the code KNOWS there is no per-token
// bill: it is a first-party CLI harness matched by User-Agent (policy
// authmode.go:40-50). OAuth deliberately does NOT count — it spans AWS SigV4
// (authmode.go:19-20), which is metered, and an unknown mode from a pre-v0.2
// ledger does not count either. A wrong "you have no bill" claim contradicts
// an invoice; a wrong cost claim only carries the assumption it already states.
//
// Strictly more than half the turns must be subscription. A tie is metered.
func (r Report) Metered() bool { return r.Modes[policy.Subscription.String()]*2 <= r.Turns }

// JoinSound reports whether the ledger and the usage records plausibly
// describe the same traffic.
//
// The tokens headroom removed were part of what the client tried to send, so
// they cannot outnumber every input token the provider billed for the same
// sessions. When they do — a partial transcript, or a test sink that never
// reported real usage — every number that combines the two sources is fiction,
// and the report says so instead of printing one.
func (r Report) JoinSound() bool {
	return r.MatchedSessions > 0 && r.TokensRemoved() <= InputTokens(r.Headroom)
}

// Build joins headroom's ledger to Claude Code's usage records.
//
// A transcript with no ledger entry becomes part of the baseline: those are
// sessions that ran WITHOUT headroom, and comparing their cache-read share
// against the proxied ones is the closest thing to a control this data has.
func Build(entries []ledger.Entry, sessions []Session) Report {
	r := Report{
		Strategies: map[string]int{},
		DriftDims:  map[string]int{},
		Reasons:    map[string]int{},
		Modes:      map[string]int{},
	}

	seenSession := map[string]bool{}
	for _, e := range entries {
		r.Turns++
		r.BytesIn += int64(e.BytesIn)
		r.BytesOut += int64(e.BytesOut)
		r.TokensBefore += e.TokensBefore
		r.TokensAfter += e.TokensAfter
		r.Replayed += e.Replayed
		r.Reasons[e.Reason]++
		r.Modes[e.Mode]++
		if e.BytesOut < e.BytesIn {
			r.CompressedTurns++
		}
		for _, s := range e.Strategies {
			r.Strategies[s]++
		}
		if len(e.Drift) > 0 {
			r.DriftTurns++
			for _, d := range e.Drift {
				r.DriftDims[d]++
			}
		}
		if !seenSession[e.Session] {
			seenSession[e.Session] = true
			r.Sessions++
		}
		if ts, err := time.Parse(time.RFC3339, e.TS); err == nil {
			if r.Since.IsZero() || ts.Before(r.Since) {
				r.Since = ts
			}
			if ts.After(r.Until) {
				r.Until = ts
			}
		}
	}

	for _, s := range sessions {
		t := s.Total()
		if seenSession[s.Digest] {
			r.MatchedSessions++
			r.Headroom = add(r.Headroom, t)
			continue
		}
		r.BaselineSessions++
		r.Baseline = add(r.Baseline, t)
	}
	return r
}

func add(a, b Usage) Usage {
	a.InputTokens += b.InputTokens
	a.CacheCreationTokens += b.CacheCreationTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.OutputTokens += b.OutputTokens
	a.CacheCreation.Ephemeral5m += b.CacheCreation.Ephemeral5m
	a.CacheCreation.Ephemeral1h += b.CacheCreation.Ephemeral1h
	return a
}

// Since drops ledger entries older than cutoff. An entry with an unparseable
// timestamp is kept: losing a turn silently would understate the report.
func Since(entries []ledger.Entry, cutoff time.Time) []ledger.Entry {
	out := entries[:0:0]
	for _, e := range entries {
		ts, err := time.Parse(time.RFC3339, e.TS)
		if err != nil || !ts.Before(cutoff) {
			out = append(out, e)
		}
	}
	return out
}
