// Package compress holds the standalone heuristic compression engines
// (LogCompressor, DiffCompressor, SearchCompressor). Each engine takes a
// ccr.Store and, when its internal CCR gate fires, stashes the original under
// ccr.ComputeKeyMD5(content) and appends its own inline retrieval marker. These
// engines are NOT transform.Transform implementations; the offload wrappers that
// adapt them to the pipeline come later.
package compress

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/adaptive"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// logLevel is a per-line severity classification driving the base score.
type logLevel int

const (
	levelUnknown logLevel = iota
	levelError
	levelFail
	levelWarn
	levelInfo
	levelDebug
	levelTrace
)

// logFormat is the detected source format; influences format-specific
// stack-trace/summary heuristics.
type logFormat int

const (
	formatGeneric logFormat = iota
	formatPytest
	formatNpm
	formatCargo
	formatJest
	formatMake
)

// LogResult is the output of LogCompressor.Compress.
type LogResult struct {
	Compressed          string
	OriginalLineCount   int
	CompressedLineCount int
	Ratio               float32 // compressedLineCount/originalLineCount
	CacheKey            string  // "" if no CCR marker emitted
}

// logCompressorConfig holds the tunable knobs (defaults from upstream).
type logCompressorConfig struct {
	maxErrors                 int
	errorContextLines         int
	keepFirstError            bool
	keepLastError             bool
	maxStackTraces            int
	stackTraceMaxLines        int
	maxWarnings               int
	dedupeWarnings            bool
	keepSummaryLines          bool
	maxTotalLines             int
	enableCCR                 bool
	minLinesForCCR            int
	minCompressionRatioForCCR float32
}

func defaultLogConfig() logCompressorConfig {
	return logCompressorConfig{
		maxErrors:                 10,
		errorContextLines:         3,
		keepFirstError:            true,
		keepLastError:             true,
		maxStackTraces:            3,
		stackTraceMaxLines:        20,
		maxWarnings:               5,
		dedupeWarnings:            true,
		keepSummaryLines:          true,
		maxTotalLines:             100,
		enableCCR:                 true,
		minLinesForCCR:            50,
		minCompressionRatioForCCR: 0.5,
	}
}

// LogCompressor is the 6-stage log/build-output compression engine.
type LogCompressor struct {
	config  logCompressorConfig
	digitRe *regexp.Regexp
	hexRe   *regexp.Regexp
	pathRe  *regexp.Regexp
}

// NewLogCompressor builds a LogCompressor with upstream-default config and the
// three precompiled normalization regexes used for warning dedup.
func NewLogCompressor() *LogCompressor {
	return &LogCompressor{
		config:  defaultLogConfig(),
		digitRe: regexp.MustCompile(`\d+`),
		hexRe:   regexp.MustCompile(`0x[0-9a-fA-F]+`),
		pathRe:  regexp.MustCompile(`/[\w/]+/`),
	}
}

// logLine is a parsed line record carrying classification + score.
type logLine struct {
	content      string
	level        logLevel
	isStackTrace bool
	isSummary    bool
	score        float32
}

// Compress runs the full 6-stage pipeline and returns the compressed result.
// When the CCR gate fires it stashes the original content under its MD5 key in
// store and appends an inline retrieval marker.
func (c *LogCompressor) Compress(content string, store ccr.Store) LogResult {
	// STAGE 1: split lines (Rust str::lines semantics).
	lines := splitLinesRust(content)
	originalLineCount := len(lines)

	if originalLineCount == 0 {
		return LogResult{
			Compressed:          content,
			OriginalLineCount:   0,
			CompressedLineCount: 0,
			Ratio:               1.0,
		}
	}

	// STAGE 1 (format detect): kept minimal for the MVP but the field exists.
	_ = c.detectFormat(lines)

	// STAGE 2: classify each line.
	parsed := make([]logLine, len(lines))
	for i, ln := range lines {
		l := logLine{content: ln}
		l.level = c.classifyLevel(ln)
		l.isStackTrace = c.isStackTraceLine(ln)
		l.isSummary = c.isSummaryLine(ln)
		// STAGE 3: score.
		l.score = scoreLogLine(l)
		parsed[i] = l
	}

	// STAGE 4: adaptive budget, clamped by maxTotalLines.
	lineStrings := make([]string, len(lines))
	copy(lineStrings, lines)
	cap := adaptive.ComputeOptimalK(lineStrings, 0.0, 5, c.config.maxTotalLines)
	if cap > c.config.maxTotalLines {
		cap = c.config.maxTotalLines
	}

	// STAGE 5: category selection + context windows + final cap.
	selected := c.selectIndices(parsed, cap)

	// STAGE 6a: format output (source order).
	out := c.formatOutput(parsed, selected)

	compressedLineCount := len(selected)
	var ratio float32
	if originalLineCount > 0 {
		ratio = float32(compressedLineCount) / float32(originalLineCount)
	} else {
		ratio = 1.0
	}

	result := LogResult{
		Compressed:          out,
		OriginalLineCount:   originalLineCount,
		CompressedLineCount: compressedLineCount,
		Ratio:               ratio,
	}

	// STAGE 6b: optional CCR storage/marker.
	if c.config.enableCCR &&
		originalLineCount >= c.config.minLinesForCCR &&
		ratio < c.config.minCompressionRatioForCCR {
		key := ccr.ComputeKeyMD5([]byte(content))
		store.Put(key, content)
		result.Compressed += fmt.Sprintf(
			"\n[%d lines compressed to %d. Retrieve more: hash=%s]",
			originalLineCount, len(selected), key,
		)
		result.CacheKey = key
	}

	return result
}

// detectFormat scans the first 100 lines, counts substring markers per format,
// and returns the highest-count format (ties / no hits -> Generic).
func (c *LogCompressor) detectFormat(lines []string) logFormat {
	sample := lines
	if len(sample) > 100 {
		sample = sample[:100]
	}
	type fm struct {
		f       logFormat
		markers []string
	}
	formats := []fm{
		{formatPytest, []string{
			"=== FAILURES", "=== ERRORS", "=== test session",
			"=== short test summary", "PASSED [", "FAILED [", "ERROR [",
			"SKIPPED [", "collected ",
		}},
		{formatNpm, []string{"npm ERR!", "npm WARN", "npm info", "npm http"}},
		{formatCargo, []string{"Compiling ", "Finished ", "Running ", "warning: ", "error[E"}},
		{formatJest, []string{"PASS ", "FAIL ", "Test Suites:"}},
		{formatMake, []string{"make[", "make:", "gcc ", "g++ ", "clang "}},
	}
	best := formatGeneric
	bestCount := 0
	for _, ff := range formats {
		count := 0
		for _, ln := range sample {
			for _, m := range ff.markers {
				if strings.Contains(ln, m) {
					count++
				}
			}
		}
		if count > bestCount {
			bestCount = count
			best = ff.f
		}
	}
	return best
}

// level keyword sets (log's OWN sets — case-sensitive enumerated variants,
// word-boundary filtered). Distinct from the signals package keyword sets.
var (
	errorLevelKeywords = []string{"ERROR", "error", "Error", "FATAL", "fatal", "Fatal", "CRITICAL", "critical"}
	failLevelKeywords  = []string{"FAIL", "FAILED", "fail", "failed", "Fail", "Failed"}
	warnLevelKeywords  = []string{"WARN", "WARNING", "warn", "warning", "Warn", "Warning"}
	infoLevelKeywords  = []string{"INFO", "info", "Info"}
	debugLevelKeywords = []string{"DEBUG", "debug", "Debug"}
	traceLevelKeywords = []string{"TRACE", "trace", "Trace"}
)

// wordByte reports whether b is a word byte ([A-Za-z0-9_]), mirroring the
// signals package boundary rule.
func wordByte(b byte) bool {
	lb := b | 0x20
	return (lb >= 'a' && lb <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// containsWord reports whether kw occurs in line as a whole word (case-sensitive,
// matching upstream's enumerated case variants), with valid word boundaries.
func containsWord(line, kw string) bool {
	from := 0
	for {
		i := strings.Index(line[from:], kw)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(kw)
		leftOK := i == 0 || !wordByte(line[i-1])
		rightOK := end >= len(line) || !wordByte(line[end])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
		if from >= len(line) {
			return false
		}
	}
}

// anyWord reports whether any keyword in kws matches as a whole word in line.
func anyWord(line string, kws []string) bool {
	for _, kw := range kws {
		if containsWord(line, kw) {
			return true
		}
	}
	return false
}

// classifyLevel detects the line's level by word-boundary keyword match in
// precedence order Error > Fail > Warn > Info > Debug > Trace, else Unknown.
func (c *LogCompressor) classifyLevel(line string) logLevel {
	switch {
	case anyWord(line, errorLevelKeywords):
		return levelError
	case anyWord(line, failLevelKeywords):
		return levelFail
	case anyWord(line, warnLevelKeywords):
		return levelWarn
	case anyWord(line, infoLevelKeywords):
		return levelInfo
	case anyWord(line, debugLevelKeywords):
		return levelDebug
	case anyWord(line, traceLevelKeywords):
		return levelTrace
	default:
		return levelUnknown
	}
}

// digitsThenColonHex matches a Go stack-frame: leading digits, ':', then "0x"
// hex (e.g. an address column in a goroutine dump).
var goFrameRe = regexp.MustCompile(`^\d+:0x[0-9a-fA-F]+`)

// rustLocRe matches a Rust diagnostic location ("--> path:LINE:COL").
var rustLocRe = regexp.MustCompile(`:\d+:\d+`)

// jsFrameSuffixRe matches a :<digits>:<digits> suffix used by JS stack frames.
var jsFrameSuffixRe = regexp.MustCompile(`:\d+:\d+`)

// isStackTraceLine performs flavor-aware (python/js/java/rust/go) stack-frame
// membership detection.
func (c *LogCompressor) isStackTraceLine(line string) bool {
	trimmed := strings.TrimSpace(line)

	// Python: Traceback header, or a frame line containing 'File "' AND '", line '.
	if strings.Contains(trimmed, "Traceback (most recent call last)") {
		return true
	}
	if strings.Contains(line, `File "`) && strings.Contains(line, `", line `) {
		return true
	}

	// JavaScript: trimmed starts with 'at ', contains '(' and ')', :<d>:<d> suffix.
	if strings.HasPrefix(trimmed, "at ") &&
		strings.Contains(trimmed, "(") && strings.Contains(trimmed, ")") &&
		jsFrameSuffixRe.MatchString(trimmed) {
		return true
	}

	// Java: starts with 'at ', contains '(', package chars limited to [A-Za-z0-9._$].
	if strings.HasPrefix(trimmed, "at ") && strings.Contains(trimmed, "(") {
		body := trimmed[len("at "):]
		if before, _, ok := strings.Cut(body, "("); ok {
			if javaPackageOK(before) {
				return true
			}
		}
	}

	// Rust: starts with '--> ' with :<digits>:<digits> suffix.
	if strings.HasPrefix(trimmed, "--> ") && rustLocRe.MatchString(trimmed) {
		return true
	}

	// Go: leading digits then ':' then '0x' + hex digits.
	if goFrameRe.MatchString(trimmed) {
		return true
	}

	return false
}

// javaPackageOK reports whether s contains only Java package chars [A-Za-z0-9._$].
func javaPackageOK(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		ok := (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '.' || b == '_' || b == '$'
		if !ok {
			return false
		}
	}
	return true
}

// summaryCountRe matches "<digits><space>passed/failed/skipped/error/warning".
var summaryCountRe = regexp.MustCompile(`^\d+ (passed|failed|skipped|error|warning)`)

// summaryTestRe matches "Test/Tests/Suite/Suites" (optional ':') followed by digits.
var summaryTestRe = regexp.MustCompile(`^(Test|Tests|Suite|Suites):?\s*\d`)

// isSummaryLine matches the summary-line heuristics.
func (c *LogCompressor) isSummaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "===") || strings.HasPrefix(trimmed, "---") {
		return true
	}
	if summaryCountRe.MatchString(trimmed) {
		return true
	}
	if summaryTestRe.MatchString(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, "TOTAL") || strings.HasPrefix(trimmed, "Total") ||
		strings.HasPrefix(trimmed, "Summary") {
		return true
	}
	if strings.HasPrefix(trimmed, "Build") || strings.HasPrefix(trimmed, "Compile") ||
		strings.HasPrefix(trimmed, "Test") {
		if strings.Contains(trimmed, "succeeded") || strings.Contains(trimmed, "failed") ||
			strings.Contains(trimmed, "complete") {
			return true
		}
	}
	return false
}

// scoreLogLine computes per-line importance: level base + stack boost + summary
// boost, capped at 1.0.
func scoreLogLine(l logLine) float32 {
	var levelScore float32
	switch l.level {
	case levelError, levelFail:
		levelScore = 1.0
	case levelWarn:
		levelScore = 0.5
	case levelInfo, levelUnknown:
		levelScore = 0.1
	case levelDebug:
		levelScore = 0.05
	case levelTrace:
		levelScore = 0.02
	}
	score := levelScore
	if l.isStackTrace {
		score += 0.3
	}
	if l.isSummary {
		score += 0.4
	}
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// normalizeForDedupe builds the warning dedup key: split content at the FIRST
// ':' or '=' into prefix+suffix; prefix verbatim; suffix normalized by the 3
// regexes in strict order (digits->N, then hex->ADDR, then paths->/PATH/). Lines
// with no ':'/'=' are treated as an all-suffix (whole line normalized).
func (c *LogCompressor) normalizeForDedupe(content string) string {
	prefix := ""
	suffix := content
	if idx := strings.IndexAny(content, ":="); idx >= 0 {
		prefix = content[:idx+1]
		suffix = content[idx+1:]
	}
	suffix = c.digitRe.ReplaceAllString(suffix, "N")
	suffix = c.hexRe.ReplaceAllString(suffix, "ADDR")
	suffix = c.pathRe.ReplaceAllString(suffix, "/PATH/")
	return prefix + suffix
}

// selectIndices runs category selection + context windows + final cap and
// returns the chosen indices in ascending (source) order.
func (c *LogCompressor) selectIndices(parsed []logLine, cap int) []int {
	keep := make(map[int]struct{})

	add := func(i int) {
		if i >= 0 && i < len(parsed) {
			keep[i] = struct{}{}
		}
	}
	addWithContext := func(i int) {
		add(i)
		for d := 1; d <= c.config.errorContextLines; d++ {
			add(i - d)
			add(i + d)
		}
	}

	// Gather category index lists.
	var errorIdx, failIdx, warnIdx, summaryIdx []int
	for i, l := range parsed {
		switch l.level {
		case levelError:
			errorIdx = append(errorIdx, i)
		case levelFail:
			failIdx = append(failIdx, i)
		case levelWarn:
			warnIdx = append(warnIdx, i)
		}
		if l.isSummary {
			summaryIdx = append(summaryIdx, i)
		}
	}

	// ERRORS / FAILS: keep first + last + top-scoring, capped at maxErrors,
	// each with a ±errorContextLines window.
	selectCategory := func(idxs []int) {
		chosen := pickCategory(parsed, idxs, c.config.maxErrors,
			c.config.keepFirstError, c.config.keepLastError)
		for _, i := range chosen {
			addWithContext(i)
		}
	}
	selectCategory(errorIdx)
	selectCategory(failIdx)

	// WARNINGS: keep up to maxWarnings, deduped via normalized-key set.
	{
		seen := make(map[string]struct{})
		kept := 0
		for _, i := range warnIdx {
			if kept >= c.config.maxWarnings {
				break
			}
			if c.config.dedupeWarnings {
				k := c.normalizeForDedupe(parsed[i].content)
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
			}
			add(i)
			kept++
		}
	}

	// STACK TRACES: keep up to maxStackTraces blocks, each truncated to
	// stackTraceMaxLines lines.
	c.selectStackTraces(parsed, add)

	// SUMMARIES: kept when keepSummaryLines.
	if c.config.keepSummaryLines {
		for _, i := range summaryIdx {
			add(i)
		}
	}

	// Final cap: if over cap, keep highest-scoring; emit in source order.
	selected := make([]int, 0, len(keep))
	for i := range keep {
		selected = append(selected, i)
	}
	if cap > 0 && len(selected) > cap {
		sort.SliceStable(selected, func(a, b int) bool {
			return parsed[selected[a]].score > parsed[selected[b]].score
		})
		selected = selected[:cap]
	}
	sort.Ints(selected)
	return selected
}

// pickCategory returns up to maxN indices from idxs: first + last (if enabled)
// plus the top-scoring remaining, dedup'd, in no particular order.
func pickCategory(parsed []logLine, idxs []int, maxN int, keepFirst, keepLast bool) []int {
	if len(idxs) == 0 || maxN <= 0 {
		return nil
	}
	chosen := make(map[int]struct{})
	if keepFirst {
		chosen[idxs[0]] = struct{}{}
	}
	if keepLast {
		chosen[idxs[len(idxs)-1]] = struct{}{}
	}
	// Top-scoring fill for the remaining slots.
	rest := make([]int, len(idxs))
	copy(rest, idxs)
	sort.SliceStable(rest, func(a, b int) bool {
		return parsed[rest[a]].score > parsed[rest[b]].score
	})
	for _, i := range rest {
		if len(chosen) >= maxN {
			break
		}
		chosen[i] = struct{}{}
	}
	out := make([]int, 0, len(chosen))
	for i := range chosen {
		out = append(out, i)
	}
	return out
}

// selectStackTraces keeps up to maxStackTraces contiguous stack-trace blocks,
// each truncated to stackTraceMaxLines lines.
func (c *LogCompressor) selectStackTraces(parsed []logLine, add func(int)) {
	blocks := 0
	i := 0
	for i < len(parsed) {
		if !parsed[i].isStackTrace {
			i++
			continue
		}
		if blocks >= c.config.maxStackTraces {
			break
		}
		// Walk the contiguous stack-trace block.
		start := i
		for i < len(parsed) && parsed[i].isStackTrace {
			i++
		}
		end := i // exclusive
		limit := end
		if end-start > c.config.stackTraceMaxLines {
			limit = start + c.config.stackTraceMaxLines
		}
		for j := start; j < limit; j++ {
			add(j)
		}
		blocks++
	}
}

// formatOutput joins selected lines (source order) with '\n' and, when lines
// were omitted, appends a footer summarizing omitted counts by level.
func (c *LogCompressor) formatOutput(parsed []logLine, selected []int) string {
	sel := make([]string, len(selected))
	for i, idx := range selected {
		sel[i] = parsed[idx].content
	}
	out := strings.Join(sel, "\n")

	omitted := len(parsed) - len(selected)
	if omitted > 0 {
		keepSet := make(map[int]struct{}, len(selected))
		for _, idx := range selected {
			keepSet[idx] = struct{}{}
		}
		var nErr, nFail, nWarn, nInfo int
		for i, l := range parsed {
			if _, ok := keepSet[i]; ok {
				continue
			}
			switch l.level {
			case levelError:
				nErr++
			case levelFail:
				nFail++
			case levelWarn:
				nWarn++
			case levelInfo, levelUnknown:
				nInfo++
			}
		}
		var parts []string
		if nErr > 0 {
			parts = append(parts, fmt.Sprintf("%d ERROR", nErr))
		}
		if nFail > 0 {
			parts = append(parts, fmt.Sprintf("%d FAIL", nFail))
		}
		if nWarn > 0 {
			parts = append(parts, fmt.Sprintf("%d WARN", nWarn))
		}
		if nInfo > 0 {
			parts = append(parts, fmt.Sprintf("%d INFO", nInfo))
		}
		footer := fmt.Sprintf("[%d lines omitted: %s]", omitted, strings.Join(parts, ", "))
		if out == "" {
			out = footer
		} else {
			out += "\n" + footer
		}
	}

	return out
}
