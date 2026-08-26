package perf

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Format renders a Report as the text `headroom perf` prints.
func Format(r Report) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	w("headroom perf   %s\n", r.LedgerPath)
	if r.Turns == 0 {
		w("\nNo turns recorded yet. Run an agent through `headroom wrap` and try again.\n")
		return b.String()
	}
	w("%s\n\n", window(r))

	w("WHAT HEADROOM DID\n")
	w("  turns             %s   (%s compressed)\n", num(r.Turns), num(r.CompressedTurns))
	w("  sessions          %s\n", num(r.Sessions))
	if modes := counted(r.Modes); modes != "" {
		w("  auth modes        %s\n", modes)
	}
	if r.TokensRemoved() > 0 {
		w("  tokens removed    %s of %s in the blocks it rewrote\n",
			num(r.TokensRemoved()), num(r.TokensBefore))
	}
	// The headline share: only when JoinSound() and TokensRemoved() > 0.
	if share := r.TokensAvoidedShare(); share >= 0 {
		if r.Metered() {
			w("  input tokens never sent   %s\n", pct(share))
		} else {
			w("  context-window headroom (cumulative)  %s\n", pct(share))
		}
	}
	w("  bytes sent        %s of %s\n", bytesOf(r.BytesOut), bytesOf(r.BytesIn))
	if saving := r.WholeBodySaving(); saving > 0 {
		w("  bytes saved       %s   (wire bytes; not tokens, not the bill)\n", pct(saving))
	}
	if r.Replayed > 0 {
		w("  blocks replayed   %s   (re-sent compressed, so the prefix stayed byte-stable)\n", num(r.Replayed))
	}
	if strategies := counted(r.Strategies); strategies != "" {
		w("  strategies        %s\n", strategies)
	}
	if outcomes := counted(r.Reasons); outcomes != "" {
		w("  outcomes          %s\n", outcomes)
	}

	w("\nWHAT THE CACHE DID          from Claude Code's own usage records\n")
	if r.MatchedSessions == 0 {
		w("  No transcript matched a ledger session, so the cache effect is unknown.\n")
		w("  This is the half that decides whether headroom helped: bytes saved with\n")
		w("  a busted cache is a loss. Check that the transcript directory is right.\n")
	} else {
		w("  sessions joined   %s\n", num(r.MatchedSessions))
		writeUsage(&b, r.Headroom)
		w("  cache-read share  %s\n", pct(CacheReadShare(r.Headroom)))
		if r.BaselineSessions > 0 {
			w("  same, unproxied   %s   over %s sessions that did not go through headroom\n",
				pct(CacheReadShare(r.Baseline)), num(r.BaselineSessions))
		}
	}
	if r.DriftTurns > 0 {
		w("  prefix rewrites   %s turns   %s\n", num(r.DriftTurns), counted(r.DriftDims))
		w("                    observed on the INBOUND body, so these are the\n")
		w("                    client's own rewrites, not headroom's\n")
	} else {
		w("  prefix rewrites   none observed\n")
	}

	w("\nVERDICT\n")
	for _, line := range verdict(r) {
		w("  %s\n", line)
	}
	return b.String()
}

func writeUsage(b *strings.Builder, u Usage) {
	total := InputTokens(u)
	fmt.Fprintf(b, "  cache read        %s tok   %s   billed %.1fx\n",
		num(u.CacheReadTokens), pct(ratio(u.CacheReadTokens, total)), PriceCacheRead)
	if u.CacheCreation.Ephemeral1h > 0 {
		fmt.Fprintf(b, "  cache write 1h    %s tok   %s   billed %.2fx\n",
			num(u.CacheCreation.Ephemeral1h), pct(ratio(u.CacheCreation.Ephemeral1h, total)), PriceCacheWrite1h)
	}
	if u.CacheCreation.Ephemeral5m > 0 {
		fmt.Fprintf(b, "  cache write 5m    %s tok   %s   billed %.2fx\n",
			num(u.CacheCreation.Ephemeral5m), pct(ratio(u.CacheCreation.Ephemeral5m, total)), PriceCacheWrite5m)
	}
	fmt.Fprintf(b, "  fresh input       %s tok   %s   billed %.1fx\n",
		num(u.InputTokens), pct(ratio(u.InputTokens, total)), PriceFreshInput)
}

// verdict states the answer in sentences, because the question the user asked
// is a yes or a no, not a table.
func verdict(r Report) []string {
	var out []string
	saving := r.WholeBodySaving()
	if saving <= 0 {
		return []string{"headroom removed nothing from what you sent. It is not earning its place."}
	}

	// Path A: MatchedSessions == 0. Mode switch is inert here.
	if r.MatchedSessions == 0 {
		out = append(out, fmt.Sprintf(
			"headroom removed %s tokens and %s of the bytes you sent, over %s turns.",
			num(r.TokensRemoved()), pct(saving), num(r.Turns)))
		out = append(out, "No transcript joined a ledger session, so there is no denominator: the share "+
			"that number is of your traffic, and what it was worth, are both withheld.")
		return out
	}

	// Path B: !JoinSound(). Mode switch is inert here too.
	if !r.JoinSound() {
		out = append(out, fmt.Sprintf("headroom removed %s of the bytes you sent, over %s turns.",
			pct(saving), num(r.Turns)))
		out = append(out, fmt.Sprintf(
			"No cache verdict: headroom removed %s tokens but the matched sessions were billed for only %s input tokens. "+
				"The ledger and the usage records are not describing the same traffic, so every number that combines them is withheld.",
			num(r.TokensRemoved()), num(InputTokens(r.Headroom))))
		return out
	}

	// Path C/D: sound join. Sentence 1 differs by mode; sentences 3, 4 are shared; sentence 2 is mode-specific.
	if r.Metered() {
		// Path C: metered
		out = append(out, fmt.Sprintf(
			"headroom kept %s input tokens out of the %s those sessions billed — %s of them.",
			num(r.TokensRemoved()), num(r.TokensRemoved()+InputTokens(r.Headroom)), pct(r.TokensAvoidedShare())))
		low, high := r.CostRange()
		out = append(out, fmt.Sprintf(
			"What that saved depends on what those tokens would have billed as, and the ledger cannot say: "+
				"between %s and %s of your input bill. The low end is a block headroom replays every turn, "+
				"which would have been a cached read at %.1fx; the high end is the turn a block is first removed, "+
				"which would have been a 1-hour cache write at %.1fx.",
			pct(low), pct(high), PriceCacheRead, PriceCacheWrite1h))
	} else {
		// Path D: subscription
		out = append(out, fmt.Sprintf(
			"headroom kept %s input tokens out of the %s pushed through the context window — %s of that traffic.",
			num(r.TokensRemoved()), num(r.TokensRemoved()+InputTokens(r.Headroom)), pct(r.TokensAvoidedShare())))
		low, high := r.CostRange()
		out = append(out, fmt.Sprintf(
			"Your plan has no per-token bill, so window space is the win, not money. "+
				"This is a share of cumulative input tokens, not of any single request's window: "+
				"the ledger records no per-turn context size, so it cannot say how much later a compaction arrives. "+
				"On a metered plan the same removal would have been worth %s to %s of the input bill.",
			pct(low), pct(high)))
	}

	// Sentence 3: cache-read share (shared between both modes)
	share := CacheReadShare(r.Headroom)
	base := CacheReadShare(r.Baseline)
	switch {
	case r.BaselineSessions == 0:
		out = append(out, fmt.Sprintf("Your cached prefix served %s of input tokens.", pct(share)))
	case share >= base:
		out = append(out, fmt.Sprintf(
			"Your cached prefix served %s of input tokens, against %s without headroom. The prefix held.",
			pct(share), pct(base)))
	default:
		out = append(out, fmt.Sprintf(
			"Your cached prefix served %s of input tokens, against %s without headroom. "+
				"That gap is a cost headroom added; compare it against the saving above.",
			pct(share), pct(base)))
	}
	// The comparison is not an experiment; say so rather than let a reader
	// treat two different workloads as a controlled A/B.
	if r.BaselineSessions > 0 {
		out = append(out, "The unproxied figure is a different set of sessions, not a control: read it as a sanity check, not a measurement.")
	}

	// Sentence 4: bytes saved (shared between both modes)
	out = append(out, fmt.Sprintf(
		"The same turns sent %s fewer bytes on the wire. That is the larger number and the one that does not bill.",
		pct(saving)))

	return out
}

func window(r Report) string {
	if r.Since.IsZero() {
		return ""
	}
	d := r.Until.Sub(r.Since).Round(time.Minute)
	return fmt.Sprintf("%s to %s  (%s)",
		r.Since.Format("2006-01-02 15:04"), r.Until.Format("2006-01-02 15:04"), d)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return -1
	}
	return float64(a) / float64(b)
}

func pct(f float64) string {
	if f < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

func num(n int) string { return group(int64(n)) }

func bytesOf(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func group(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

func counted(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			keys = append(keys, k)
		}
	}
	// Most frequent first, name as the tiebreak so the output is stable.
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
