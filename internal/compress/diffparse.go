package compress

import (
	"path"
	"sort"
	"strconv"
	"strings"
)

// hunk is one @@ block plus the file-header lines that introduce it. The
// header travels with the hunk so that dropping a hunk cannot orphan the
// "diff --git" line that named its file.
type hunk struct {
	file   string
	header []string
	body   []string
}

// lockfiles are dependency-manifest files whose diffs are almost always noise
// to a reader: large, mechanical, and derived from another file's change.
var lockfiles = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"Gemfile.lock":      true,
	"composer.lock":     true,
}

func isLockfile(p string) bool { return lockfiles[path.Base(p)] }

// isFileHeader reports whether a line introduces a file rather than content.
func isFileHeader(line string) bool {
	return strings.HasPrefix(line, "diff --git ") ||
		strings.HasPrefix(line, "index ") ||
		strings.HasPrefix(line, "--- ") ||
		strings.HasPrefix(line, "+++ ") ||
		strings.HasPrefix(line, "new file mode ") ||
		strings.HasPrefix(line, "deleted file mode ") ||
		strings.HasPrefix(line, "similarity index ") ||
		strings.HasPrefix(line, "rename ")
}

// fileFromHeader pulls a path out of a "diff --git a/x b/x" or "+++ b/x" line.
func fileFromHeader(line string) (string, bool) {
	if strings.HasPrefix(line, "diff --git ") {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			return strings.TrimPrefix(fields[3], "b/"), true
		}
		return "", false
	}
	if strings.HasPrefix(line, "+++ ") {
		p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		if p == "/dev/null" {
			return "", false
		}
		return strings.TrimPrefix(p, "b/"), true
	}
	return "", false
}

// hunkLineCount parses one side of a "@@ -a,b +c,d @@" range (the "-a,b" or
// "+c,d" field) and returns its line count. A count omitted from the range
// (e.g. "-1" instead of "-1,1") means 1 line, per the unified diff format.
// Anything unparseable also yields 1, so a malformed header can't wedge the
// hunk open forever.
func hunkLineCount(field string) int {
	field = strings.TrimPrefix(strings.TrimPrefix(field, "-"), "+")
	if _, count, ok := strings.Cut(field, ","); ok {
		if n, err := strconv.Atoi(count); err == nil {
			return n
		}
	}
	return 1
}

// hunkCounts parses the old/new line counts from a "@@ -a,b +c,d @@" header.
func hunkCounts(line string) (oldCount, newCount int) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 1, 1
	}
	return hunkLineCount(fields[1]), hunkLineCount(fields[2])
}

// parseDiff splits a unified diff into the text before the first file header
// and one hunk per @@ block. Input that is not a diff yields no hunks, which
// the caller treats as "nothing to do".
func parseDiff(s string) ([]string, []hunk) {
	if s == "" {
		return nil, nil
	}
	lines := strings.Split(s, "\n")

	var preamble []string
	var hunks []hunk
	var pending []string // file-header lines seen since the last hunk
	var current *hunk
	var oldRemaining, newRemaining int // body lines still owed to the open hunk
	file := ""
	started := false

	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}

	for _, line := range lines {
		switch {
		// File-header prefixes only count outside a hunk body (current ==
		// nil, i.e. the hunk's @@ line counts have already been satisfied),
		// where a deleted line (e.g. "-- old comment" -> "--- old comment")
		// could otherwise false-positive as a "---" file marker and truncate
		// the hunk early. "diff --git " is the one exception: it starts a
		// new file section unconditionally, and a deleted line can never
		// render as "diff --git ..." (it would render as "-diff --git ...").
		case (current == nil && isFileHeader(line)), strings.HasPrefix(line, "diff --git "):
			flush()
			started = true
			if f, ok := fileFromHeader(line); ok {
				file = f
			}
			pending = append(pending, line)

		case strings.HasPrefix(line, "@@"):
			flush()
			started = true
			oldRemaining, newRemaining = hunkCounts(line)
			current = &hunk{file: file, header: pending, body: []string{line}}
			pending = nil

		default:
			if current != nil {
				current.body = append(current.body, line)
				switch {
				case strings.HasPrefix(line, "-"):
					oldRemaining--
				case strings.HasPrefix(line, "+"):
					newRemaining--
				case strings.HasPrefix(line, "\\"):
					// "\ No newline at end of file" consumes neither side.
				default:
					oldRemaining--
					newRemaining--
				}
				if oldRemaining <= 0 && newRemaining <= 0 {
					flush()
				}
			} else if started {
				pending = append(pending, line)
			} else {
				preamble = append(preamble, line)
			}
		}
	}
	flush()
	// A file section with no @@ block (binary files, mode-only changes,
	// empty file creation) leaves its header lines in pending with no hunk
	// to carry them. Wrap them in a headers-only hunk so they survive
	// instead of being dropped on the floor.
	if len(pending) > 0 {
		hunks = append(hunks, hunk{file: file, header: pending})
	}
	return preamble, hunks
}

// contentLines splits a hunk body into its added and removed content,
// excluding the @@ line and the --- / +++ file markers.
func contentLines(h hunk) (added, removed []string) {
	for _, line := range h.body {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "+"):
			added = append(added, line[1:])
		case strings.HasPrefix(line, "-"):
			removed = append(removed, line[1:])
		}
	}
	return added, removed
}

// stripAllWhitespace removes every space, tab, and carriage return so two
// lines differing only in indentation compare equal.
func stripAllWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r':
			return -1
		}
		return r
	}, s)
}

// isWhitespaceOnly reports whether a hunk's added and removed lines are the
// same multiset once whitespace is removed. Sorting makes reordered-but-
// identical sets compare equal and keeps the result deterministic.
func isWhitespaceOnly(h hunk) bool {
	added, removed := contentLines(h)
	if len(added) == 0 || len(added) != len(removed) {
		return false
	}
	a := make([]string, len(added))
	r := make([]string, len(removed))
	for i := range added {
		a[i] = stripAllWhitespace(added[i])
		r[i] = stripAllWhitespace(removed[i])
	}
	sort.Strings(a)
	sort.Strings(r)
	for i := range a {
		if a[i] != r[i] {
			return false
		}
	}
	return true
}

// isNoise reports whether a hunk carries no information a reader needs.
func isNoise(h hunk) bool { return isLockfile(h.file) || isWhitespaceOnly(h) }
