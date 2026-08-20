// Package compress holds information-preserving offload transforms. Each one
// stashes the original in the CCR store before it drops anything, so every
// drop is recoverable through the emitted CacheKey.
package compress

import (
	"fmt"
	"strings"
)

// ansiCSI matches a CSI escape: ESC [ , parameter bytes, intermediate bytes,
// then a final byte in @-~. Written as an explicit character class rather than
// a regexp so the hot path does no regexp work.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f { // parameter bytes
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f { // intermediate bytes
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e { // final byte
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// collapseRuns folds two or more consecutive identical lines into the first
// line plus a repeat count.
func collapseRuns(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		out = append(out, lines[i])
		if n := j - i; n > 1 {
			out = append(out, fmt.Sprintf("... previous line repeated %d more times", n-1))
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// asciiLower lowercases ASCII letters only. It is length-preserving byte for
// byte, unlike strings.ToLower, so indexes found in the result stay valid
// against the original string even when that string holds invalid UTF-8 or
// non-ASCII bytes.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// warningBody returns the text after a warning marker, and whether the line is
// a warning at all. Matching is case-insensitive on the marker only; the body
// keeps its original case so distinct warnings stay distinct.
func warningBody(line string) (string, bool) {
	lower := asciiLower(line)
	for _, marker := range []string{"warning:", "warn:"} {
		if i := strings.Index(lower, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):]), true
		}
	}
	return "", false
}

// dedupWarnings keeps the first occurrence of each distinct warning in place,
// drops the repeats, and appends one summary line when anything was dropped.
//
// The summary goes at the end rather than in place so that removing a repeat
// never shifts the position of an unrelated line. Iteration is over the line
// slice, never a map, so the result is deterministic.
func dedupWarnings(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	seen := make(map[string]bool, len(lines))
	out := make([]string, 0, len(lines))

	dropped, distinct := 0, 0
	for _, line := range lines {
		body, isWarning := warningBody(line)
		if !isWarning {
			out = append(out, line)
			continue
		}
		if !seen[body] {
			seen[body] = true
			out = append(out, line)
			continue
		}
		dropped++
	}
	if dropped > 0 {
		// Count how many distinct bodies were duplicated, by re-walking the
		// lines rather than the map, to keep the count deterministic.
		dupOf := map[string]int{}
		for _, line := range lines {
			if body, ok := warningBody(line); ok {
				dupOf[body]++
			}
		}
		for _, line := range lines {
			if body, ok := warningBody(line); ok && dupOf[body] > 1 {
				dupOf[body] = 0 // count each distinct body once
				distinct++
			}
		}
		out = append(out, fmt.Sprintf("... %d more occurrences of %d duplicated warning", dropped, distinct))
	}
	return strings.Join(out, "\n")
}

// dropProgress removes lines a terminal would have overwritten: those holding
// a carriage return that is not the line terminator.
func dropProgress(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// A single trailing \r is a CRLF terminator, not overwritten output.
		if strings.Contains(strings.TrimSuffix(line, "\r"), "\r") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
