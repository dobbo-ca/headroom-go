package compress

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// DiffResult is the output of DiffCompressor.Compress. Line counts use Python
// strings.Split(content,"\n") semantics. additions/deletions are summed from the
// ORIGINAL (pre-trim, pre-cap) hunk counts.
type DiffResult struct {
	Compressed          string
	OriginalLineCount   int
	CompressedLineCount int // captured BEFORE the CCR marker is appended
	FilesAffected       int
	Additions           int
	Deletions           int
	HunksKept           int
	HunksRemoved        int
	CacheKey            string // "" if no CCR marker emitted
}

// diffCompressorConfig holds the tunable knobs (defaults from upstream).
// alwaysKeepAdditions/alwaysKeepDeletions are RESERVED/unused (the algorithm
// always keeps +/- lines); kept for config-schema parity.
type diffCompressorConfig struct {
	maxContextLines           int
	maxHunksPerFile           int
	maxFiles                  int
	alwaysKeepAdditions       bool
	alwaysKeepDeletions       bool
	enableCCR                 bool
	minLinesForCCR            int
	minCompressionRatioForCCR float64
}

func defaultDiffConfig() diffCompressorConfig {
	return diffCompressorConfig{
		maxContextLines:           2,
		maxHunksPerFile:           10,
		maxFiles:                  20,
		alwaysKeepAdditions:       true,
		alwaysKeepDeletions:       true,
		enableCCR:                 true,
		minLinesForCCR:            50,
		minCompressionRatioForCCR: 0.8,
	}
}

// DiffCompressor is the unified-diff compression engine: parse -> score ->
// file-cap -> per-file hunk select/context-trim -> format, with an inline CCR
// retrieval marker when the line-count ratio gate passes.
type DiffCompressor struct {
	config diffCompressorConfig

	hunkHeaderRe   *regexp.Regexp
	hunkNewRangeRe *regexp.Regexp
	diffGitRe      *regexp.Regexp
	diffCombinedRe *regexp.Regexp
	diffCcRe       *regexp.Regexp
	oldFileRe      *regexp.Regexp
	newFileRe      *regexp.Regexp
	binaryRe       *regexp.Regexp
	priorityRes    []*regexp.Regexp
}

// NewDiffCompressor builds a DiffCompressor with upstream-default config and the
// precompiled parse/scoring regexes.
func NewDiffCompressor() *DiffCompressor {
	return &DiffCompressor{
		config:         defaultDiffConfig(),
		hunkHeaderRe:   regexp.MustCompile(`^(?:@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@|@@@ -\d+(?:,\d+)? -\d+(?:,\d+)? \+\d+(?:,\d+)? @@@|@@@@ -\d+(?:,\d+)? -\d+(?:,\d+)? -\d+(?:,\d+)? \+\d+(?:,\d+)? @@@@)(.*)$`),
		hunkNewRangeRe: regexp.MustCompile(`\+(\d+)`),
		diffGitRe:      regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`),
		diffCombinedRe: regexp.MustCompile(`^diff --combined (.+)$`),
		diffCcRe:       regexp.MustCompile(`^diff --cc (.+)$`),
		oldFileRe:      regexp.MustCompile(`^--- (a/(.+)|/dev/null)$`),
		newFileRe:      regexp.MustCompile(`^\+\+\+ (b/(.+)|/dev/null)$`),
		binaryRe:       regexp.MustCompile(`^Binary files .+ differ$`),
		priorityRes: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(error|exception|fail(?:ed|ure)?|fatal|critical|crash|panic)\b`),
			regexp.MustCompile(`(?i)\b(important|note|todo|fixme|hack|xxx|bug|fix)\b`),
			regexp.MustCompile(`(?i)\b(security|auth|password|secret|token)\b`),
		},
	}
}

// diffHunk is one hunk: header, body lines, and per-line classification counts.
type diffHunk struct {
	header       string
	lines        []string
	additions    int
	deletions    int
	contextLines int
	score        float64
}

// diffFile is one file section parsed from the diff.
type diffFile struct {
	header                      string
	oldFile                     string // full "--- ..." line (may be empty)
	newFile                     string // full "+++ ..." line (may be empty)
	hunks                       []*diffHunk
	isNewFile                   bool
	isDeletedFile               bool
	isRenamed                   bool
	isBinary                    bool
	renameLines                 []string
	originalNewFileModeLine     string
	hasOriginalNewFileModeLine  bool
	originalDeletedFileModeLine string
	hasOriginalDeletedModeLine  bool
	originalBinaryLine          string
	hasOriginalBinaryLine       bool
}

func (f *diffFile) totalAdditions() int {
	n := 0
	for _, h := range f.hunks {
		n += h.additions
	}
	return n
}

func (f *diffFile) totalDeletions() int {
	n := 0
	for _, h := range f.hunks {
		n += h.deletions
	}
	return n
}

// parsedDiff is the result of parse: pre-diff preamble lines plus file sections.
type parsedDiff struct {
	preDiffLines []string
	files        []*diffFile
}

// Compress runs the full diff-compression pipeline. When the CCR gate fires it
// stashes the original content under its MD5 key in store and appends an inline
// retrieval marker. context is an optional user query for relevance scoring.
func (c *DiffCompressor) Compress(content, context string, store ccr.Store) DiffResult {
	// STAGE 0: split lines (Python str.split semantics).
	lines := strings.Split(content, "\n")
	originalLineCount := len(lines)

	// SHORT-CIRCUIT 1: size gate (min_lines_for_ccr gates the WHOLE path).
	if originalLineCount < c.config.minLinesForCCR {
		return passThroughDiff(content, originalLineCount)
	}

	// STAGE 1: parse.
	pd := c.parseDiff(lines)

	// SHORT-CIRCUIT 2: no-diff gate.
	if len(pd.files) == 0 {
		return passThroughDiff(content, originalLineCount)
	}

	// STAGE 2: score hunks.
	c.scoreHunks(pd.files, context)

	// STAGE 3: file cap.
	if len(pd.files) > c.config.maxFiles {
		sort.SliceStable(pd.files, func(a, b int) bool {
			ca := pd.files[a].totalAdditions() + pd.files[a].totalDeletions()
			cb := pd.files[b].totalAdditions() + pd.files[b].totalDeletions()
			return cb < ca // descending by change count
		})
		pd.files = pd.files[:c.config.maxFiles]
	}

	// STAGE 5: per-file compression. (Stage 4 = observability-only; omitted.)
	totalAdditions := 0
	totalDeletions := 0
	hunksKeptTotal := 0
	hunksRemovedTotal := 0
	for _, f := range pd.files {
		// Sums from ORIGINAL hunk counts (before cap/trim).
		totalAdditions += f.totalAdditions()
		totalDeletions += f.totalDeletions()
		originalHunkCount := len(f.hunks)

		selected, _ := c.selectHunks(f.hunks)

		compressed := make([]*diffHunk, 0, len(selected))
		for _, h := range selected {
			compressed = append(compressed, c.reduceContext(h))
		}
		hunksKeptTotal += len(compressed)
		hunksRemovedTotal += originalHunkCount - len(compressed)
		f.hunks = compressed
	}

	filesAffected := len(pd.files)

	// STAGE 7: format output; capture compressed line count BEFORE the marker.
	out := c.formatOutput(pd.preDiffLines, pd.files, filesAffected, totalAdditions, totalDeletions, hunksRemovedTotal)
	compressedLineCount := len(strings.Split(out, "\n"))

	result := DiffResult{
		Compressed:          out,
		OriginalLineCount:   originalLineCount,
		CompressedLineCount: compressedLineCount,
		FilesAffected:       filesAffected,
		Additions:           totalAdditions,
		Deletions:           totalDeletions,
		HunksKept:           hunksKeptTotal,
		HunksRemoved:        hunksRemovedTotal,
	}

	// STAGE 8: CCR gate + marker.
	if c.config.enableCCR &&
		float64(compressedLineCount) < float64(originalLineCount)*c.config.minCompressionRatioForCCR {
		key := ccr.ComputeKeyMD5([]byte(content))
		result.Compressed += fmt.Sprintf(
			"\n[%d lines compressed to %d. Retrieve full diff: hash=%s]",
			originalLineCount, compressedLineCount, key,
		)
		store.Put(key, content)
		result.CacheKey = key
	}

	return result
}

// passThroughDiff returns content verbatim with no compression (the two
// short-circuit paths). compressed=content, files_affected=0, no cache_key.
func passThroughDiff(content string, originalLineCount int) DiffResult {
	return DiffResult{
		Compressed:          content,
		OriginalLineCount:   originalLineCount,
		CompressedLineCount: originalLineCount,
	}
}

// isDiffHeader reports whether line is a diff section header (git OR combined OR cc).
func (c *DiffCompressor) isDiffHeader(line string) bool {
	return c.diffGitRe.MatchString(line) ||
		c.diffCombinedRe.MatchString(line) ||
		c.diffCcRe.MatchString(line)
}

// parseDiff walks lines tracking the current file/hunk and produces the parsed
// structure. See research §13 for the exact state machine.
func (c *DiffCompressor) parseDiff(lines []string) parsedDiff {
	var pd parsedDiff
	var currentFile *diffFile
	var currentHunk *diffHunk

	flushHunk := func() {
		if currentFile != nil && currentHunk != nil {
			currentFile.hunks = append(currentFile.hunks, currentHunk)
		}
		currentHunk = nil
	}
	flushFile := func() {
		flushHunk()
		if currentFile != nil {
			pd.files = append(pd.files, currentFile)
		}
		currentFile = nil
	}

	for _, line := range lines {
		// (1) Diff section header.
		if c.isDiffHeader(line) {
			flushFile()
			currentFile = &diffFile{header: line}
			continue
		}

		// (2) Preamble (no current file yet).
		if currentFile == nil {
			pd.preDiffLines = append(pd.preDiffLines, line)
			continue
		}

		// (3) File-level markers (not continue-guarded as a block; falls through
		// to the regex checks below if none match).
		if strings.HasPrefix(line, "new file mode") {
			currentFile.isNewFile = true
			currentFile.originalNewFileModeLine = line
			currentFile.hasOriginalNewFileModeLine = true
		} else if strings.HasPrefix(line, "deleted file mode") {
			currentFile.isDeletedFile = true
			currentFile.originalDeletedFileModeLine = line
			currentFile.hasOriginalDeletedModeLine = true
		} else if strings.HasPrefix(line, "rename ") ||
			strings.HasPrefix(line, "similarity ") ||
			strings.HasPrefix(line, "copy ") ||
			strings.HasPrefix(line, "dissimilarity ") {
			currentFile.isRenamed = true
			currentFile.renameLines = append(currentFile.renameLines, line)
		} else if c.binaryRe.MatchString(line) {
			currentFile.isBinary = true
			currentFile.originalBinaryLine = line
			currentFile.hasOriginalBinaryLine = true
		}

		// (4) Old-file line.
		if c.oldFileRe.MatchString(line) {
			currentFile.oldFile = line
			continue
		}
		// (5) New-file line.
		if c.newFileRe.MatchString(line) {
			currentFile.newFile = line
			continue
		}
		// (6) Hunk header.
		if c.hunkHeaderRe.MatchString(line) {
			flushHunk()
			currentHunk = &diffHunk{header: line}
			continue
		}
		// (7) Hunk content.
		if currentHunk != nil {
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				currentHunk.additions++
				currentHunk.lines = append(currentHunk.lines, line)
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				currentHunk.deletions++
				currentHunk.lines = append(currentHunk.lines, line)
			case strings.HasPrefix(line, " ") || line == "":
				currentHunk.contextLines++
				currentHunk.lines = append(currentHunk.lines, line)
			default:
				// "other" line (e.g. backslash no-newline marker): verbatim, no count.
				currentHunk.lines = append(currentHunk.lines, line)
			}
		}
	}

	flushFile()
	return pd
}

// scoreHunks annotates every hunk with a relevance score in [0,1]. See research §14.
func (c *DiffCompressor) scoreHunks(files []*diffFile, context string) {
	contextLower := strings.ToLower(context)
	contextWords := strings.Fields(contextLower)

	for _, f := range files {
		for _, h := range f.hunks {
			score := float64(h.additions+h.deletions) * 0.03
			if score > 0.3 {
				score = 0.3
			}
			contentLower := strings.ToLower(strings.Join(h.lines, "\n"))
			for _, w := range contextWords {
				if len(w) > 2 && strings.Contains(contentLower, w) {
					score += 0.2
				}
			}
			for _, pat := range c.priorityRes {
				if pat.MatchString(contentLower) {
					score += 0.3
					break
				}
			}
			if score > 1.0 {
				score = 1.0
			}
			h.score = score
		}
	}
}

// selectHunks keeps first + last + top-scored middle (up to max_hunks_per_file),
// then re-sorts the kept hunks by new-range line number ascending. Returns
// (selected, dropped). See research §15.
func (c *DiffCompressor) selectHunks(hunks []*diffHunk) (selected, dropped []*diffHunk) {
	maxPerFile := c.config.maxHunksPerFile
	if len(hunks) <= maxPerFile {
		return hunks, nil
	}
	if len(hunks) == 0 {
		return nil, nil
	}

	first := hunks[0]
	last := hunks[len(hunks)-1]
	middle := make([]*diffHunk, len(hunks)-2)
	copy(middle, hunks[1:len(hunks)-1])

	// remaining_slots: maxPerFile-2 when a distinct last exists (always here,
	// since len > maxPerFile >= 1 implies at least 2 hunks). saturating_sub.
	remainingSlots := maxPerFile - 2
	if remainingSlots < 0 {
		remainingSlots = 0
	}

	// Stable sort middle by score DESCENDING (NaN->Equal tiebreak; Go floats
	// here are never NaN, equal scores keep input order via SliceStable).
	sort.SliceStable(middle, func(a, b int) bool {
		return middle[b].score < middle[a].score
	})

	keptMiddle := make([]*diffHunk, 0, remainingSlots)
	for i, h := range middle {
		if i < remainingSlots {
			keptMiddle = append(keptMiddle, h)
		} else {
			dropped = append(dropped, h)
		}
	}

	selected = make([]*diffHunk, 0, len(keptMiddle)+2)
	selected = append(selected, first)
	selected = append(selected, keptMiddle...)
	selected = append(selected, last)

	// Re-sort selected by extract_line_number ascending (stable) to restore order.
	sort.SliceStable(selected, func(a, b int) bool {
		return c.extractLineNumber(selected[a].header) < c.extractLineNumber(selected[b].header)
	})

	return selected, dropped
}

// extractLineNumber pulls the +N new-file start line from a hunk header; returns
// 0 on any failure.
func (c *DiffCompressor) extractLineNumber(header string) int {
	m := c.hunkNewRangeRe.FindStringSubmatch(header)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// reduceContext trims a hunk to +/-max_context_lines around each change, always
// keeping backslash no-newline markers; recounts. See research §17.
func (c *DiffCompressor) reduceContext(h *diffHunk) *diffHunk {
	maxContext := c.config.maxContextLines

	// change positions: lines starting with '+' or '-'.
	var changePositions []int
	for i, ln := range h.lines {
		if strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-") {
			changePositions = append(changePositions, i)
		}
	}

	if len(changePositions) == 0 {
		take := maxContext
		if take > len(h.lines) {
			take = len(h.lines)
		}
		newLines := make([]string, take)
		copy(newLines, h.lines[:take])
		return &diffHunk{
			header:       h.header,
			lines:        newLines,
			additions:    0,
			deletions:    0,
			contextLines: take,
			score:        h.score,
		}
	}

	keep := make(map[int]bool)
	for _, pos := range changePositions {
		keep[pos] = true
		lo := pos - maxContext
		if lo < 0 {
			lo = 0
		}
		for i := lo; i < pos; i++ {
			keep[i] = true
		}
		hi := pos + maxContext + 1
		if hi > len(h.lines) {
			hi = len(h.lines)
		}
		for i := pos + 1; i < hi; i++ {
			keep[i] = true
		}
	}
	// Force-keep backslash-prefixed lines regardless of distance.
	for i, ln := range h.lines {
		if strings.HasPrefix(ln, "\\") {
			keep[i] = true
		}
	}

	idxs := make([]int, 0, len(keep))
	for i := range keep {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	nh := &diffHunk{header: h.header, score: h.score}
	for _, i := range idxs {
		ln := h.lines[i]
		nh.lines = append(nh.lines, ln)
		switch {
		case strings.HasPrefix(ln, "+"):
			nh.additions++
		case strings.HasPrefix(ln, "-"):
			nh.deletions++
		default:
			nh.contextLines++
		}
	}
	return nh
}

// formatOutput renders the compressed diff. See research §18.
func (c *DiffCompressor) formatOutput(preDiffLines []string, files []*diffFile, filesAffected, totalAdditions, totalDeletions, hunksRemoved int) string {
	var out []string

	// (1) Pre-diff preamble verbatim.
	out = append(out, preDiffLines...)

	// (2) Per file.
	for _, f := range files {
		out = append(out, f.header)
		out = append(out, f.renameLines...)
		if f.isNewFile {
			out = append(out, "new file mode 100644")
		} else if f.isDeletedFile {
			out = append(out, "deleted file mode 100644")
		}
		if f.isBinary {
			out = append(out, "Binary files differ")
			continue // skip hunks for binary files
		}
		if f.oldFile != "" {
			out = append(out, f.oldFile)
		}
		if f.newFile != "" {
			out = append(out, f.newFile)
		}
		for _, h := range f.hunks {
			out = append(out, h.header)
			out = append(out, h.lines...)
		}
	}

	// (3) Footer.
	if hunksRemoved > 0 || filesAffected > 0 {
		parts := []string{
			fmt.Sprintf("%d files changed", filesAffected),
			fmt.Sprintf("+%d -%d lines", totalAdditions, totalDeletions),
		}
		if hunksRemoved > 0 {
			parts = append(parts, fmt.Sprintf("%d hunks omitted", hunksRemoved))
		}
		out = append(out, "["+strings.Join(parts, ", ")+"]")
	}

	return strings.Join(out, "\n")
}
