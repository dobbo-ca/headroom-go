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
	w("  bytes sent        %s of %s\n", bytesOf(r.BytesOut), bytesOf(r.BytesIn))
	w("  whole-body saving %s   <- what a user actually sees\n", pct(r.WholeBodySaving()))
	if r.TokensRemoved() > 0 {
		w("  tokens removed    %s of %s reachable   %s\n",
			num(r.TokensRemoved()), num(r.TokensBefore), pct(ratio(r.TokensRemoved(), r.TokensBefore)))
	}
	if r.Replayed > 0 {
		w("  blocks replayed   %s   (re-sent compressed, so the prefix stayed byte-stable)\n", num(r.Replayed))
	}
	if len(r.Strategies) > 0 {
		w("  strategies        %s\n", counted(r.Strategies))
	}
	if len(r.Reasons) > 0 {
		w("  outcomes          %s\n", counted(r.Reasons))
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
	out = append(out, fmt.Sprintf("headroom removed %s of the bytes you sent, over %s turns.",
		pct(saving), num(r.Turns)))

	if r.MatchedSessions == 0 {
		out = append(out, "The cache effect is unmeasured, so this number is only half an answer.")
		return out
	}

	if !r.JoinSound() {
		out = append(out, fmt.Sprintf(
			"No cache verdict: headroom removed %s tokens but the matched sessions were billed for only %s input tokens. "+
				"The ledger and the usage records are not describing the same traffic, so every number that combines them is withheld.",
			num(r.TokensRemoved()), num(InputTokens(r.Headroom))))
		return out
	}

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

	switch avoided := r.InputCostAvoided(); {
	case avoided > 0:
		out = append(out, fmt.Sprintf(
			"Priced at Anthropic's cache multipliers, that is about %s of input cost avoided, "+
				"assuming the removed tokens would have billed at the same average rate as the ones that stayed.",
			pct(avoided)))
	}
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
		keys = append(keys, k)
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
