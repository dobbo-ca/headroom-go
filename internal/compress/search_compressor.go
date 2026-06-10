package compress

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/adaptive"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/signals"
)

// SearchResult is the output of SearchCompressor.Compress. CompressionRatio is a
// BYTE ratio: len(body)/max(1,len(content)). CacheKey is "" when no CCR marker
// was emitted.
type SearchResult struct {
	Compressed           string
	OriginalMatchCount   int
	CompressedMatchCount int
	FilesAffected        int
	CompressionRatio     float64
	CacheKey             string
}

// searchCompressorConfig holds the tunable knobs (defaults from upstream).
// contextKeywords is empty by default; each substring hit adds +0.4 to a match
// score.
type searchCompressorConfig struct {
	maxMatchesPerFile         int
	alwaysKeepFirst           bool
	alwaysKeepLast            bool
	maxTotalMatches           int
	maxFiles                  int
	contextKeywords           []string
	boostErrors               bool
	enableCCR                 bool
	minMatchesForCCR          int
	minCompressionRatioForCCR float64
}

func defaultSearchConfig() searchCompressorConfig {
	return searchCompressorConfig{
		maxMatchesPerFile:         5,
		alwaysKeepFirst:           true,
		alwaysKeepLast:            true,
		maxTotalMatches:           30,
		maxFiles:                  15,
		contextKeywords:           nil,
		boostErrors:               true,
		enableCCR:                 true,
		minMatchesForCCR:          10,
		minCompressionRatioForCCR: 0.8,
	}
}

// SearchCompressor is the grep/ripgrep/ag output compression engine: byte-scan
// parse -> score -> file-cap + adaptive select -> format, with an inline CCR
// retrieval marker when the byte-ratio gate passes.
type SearchCompressor struct {
	config   searchCompressorConfig
	detector signals.LineImportanceDetector
}

// NewSearchCompressor builds a SearchCompressor with upstream-default config and
// the default KeywordDetector for error/warning boosting.
func NewSearchCompressor() *SearchCompressor {
	return &SearchCompressor{
		config:   defaultSearchConfig(),
		detector: signals.NewKeywordDetector(),
	}
}

// searchMatch is one parsed match line.
type searchMatch struct {
	file    string
	lineNo  uint64
	content string
	score   float64
}

// fileMatches holds all matches for one file (key = file path).
type fileMatches struct {
	file    string
	matches []searchMatch
}

func (fm *fileMatches) totalScore() float64 {
	s := 0.0
	for _, m := range fm.matches {
		s += m.score
	}
	return s
}

// Compress runs the full search-compression pipeline. When the CCR gate fires it
// stashes the original content under its MD5 key in store and appends an inline
// retrieval marker. context is an optional user query for relevance scoring.
func (c *SearchCompressor) Compress(content, context string, bias float64, store ccr.Store) SearchResult {
	// STAGE 1: parse.
	parsed := c.parseSearchResults(content)

	// STAGE 2: early guard — no matches parsed.
	if len(parsed) == 0 {
		return SearchResult{
			Compressed:       content,
			CompressionRatio: 1.0,
		}
	}

	// STAGE 3: count original matches.
	originalCount := 0
	for _, fm := range parsed {
		originalCount += len(fm.matches)
	}

	// STAGE 4: score.
	c.scoreMatches(parsed, context)

	// STAGE 5: select.
	selected, filesAffected := c.selectMatches(parsed, bias)

	// STAGE 6: format.
	body := c.formatOutput(selected, parsed)

	// STAGE 7: counts + byte ratio.
	compressedCount := 0
	for _, fm := range selected {
		compressedCount += len(fm.matches)
	}
	denom := len(content)
	if denom < 1 {
		denom = 1
	}
	ratio := float64(len(body)) / float64(denom)

	result := SearchResult{
		Compressed:           body,
		OriginalMatchCount:   originalCount,
		CompressedMatchCount: compressedCount,
		FilesAffected:        filesAffected,
		CompressionRatio:     ratio,
	}

	// STAGE 8: CCR gate + marker.
	if c.config.enableCCR &&
		originalCount >= c.config.minMatchesForCCR &&
		ratio < c.config.minCompressionRatioForCCR &&
		store != nil {
		key := ccr.ComputeKeyMD5([]byte(content))
		store.Put(key, content)
		result.Compressed += fmt.Sprintf(
			"\n[%d matches compressed to %d. Retrieve more: hash=%s]",
			originalCount, compressedCount, key,
		)
		result.CacheKey = key
	}

	return result
}

// parseSearchResults splits content on '\n', trims each line, skips empties,
// parses each line, and groups matches by file path. Returns files in sorted
// path order.
func (c *SearchCompressor) parseSearchResults(content string) []*fileMatches {
	byPath := make(map[string]*fileMatches)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		file, lineNo, body, ok := parseMatchLine(line)
		if !ok {
			continue
		}
		fm, exists := byPath[file]
		if !exists {
			fm = &fileMatches{file: file}
			byPath[file] = fm
		}
		fm.matches = append(fm.matches, searchMatch{file: file, lineNo: lineNo, content: body})
	}

	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]*fileMatches, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

// parseMatchLine is a byte-scan parser (NO regex) for "path:line:content" (no
// column). Separators are BOTH ':' and '-'; the leftmost valid <sep><digits><sep>
// triplet wins. A Windows-drive guard skips a "C:\" / "C:/" prefix. Returns false
// for unparseable lines.
func parseMatchLine(line string) (file string, lineNo uint64, content string, ok bool) {
	b := []byte(line)
	n := len(b)

	// Windows-drive guard: skip past the drive colon (C:\ or C:/).
	scanStart := 0
	if n >= 3 && asciiAlpha(b[0]) && b[1] == ':' && (b[2] == '\\' || b[2] == '/') {
		scanStart = 2
	}

	i := scanStart
	for i < n {
		if b[i] == ':' || b[i] == '-' {
			// Skip runs of consecutive separators.
			if i > 0 && (b[i-1] == ':' || b[i-1] == '-') {
				i++
				continue
			}
			ds := i + 1
			j := ds
			for j < n && asciiDigit(b[j]) {
				j++
			}
			if j > ds && j < n && (b[j] == ':' || b[j] == '-') {
				if i == 0 {
					return "", 0, "", false // zero-length path
				}
				num, err := strconv.ParseUint(line[ds:j], 10, 64)
				if err != nil {
					return "", 0, "", false
				}
				return line[:i], num, line[j+1:], true
			}
		}
		i++
	}
	return "", 0, "", false
}

func asciiAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func asciiDigit(c byte) bool { return c >= '0' && c <= '9' }

// scoreMatches assigns each match a score in [0,1]: +0.3 per context word
// (len>2, lowercased substring), +importance category bump when boostErrors,
// +0.4 per config keyword substring hit; clamp 1.0.
// asciiLower lowercases only ASCII A-Z, leaving bytes >= 0x80 untouched. This
// mirrors Rust's to_ascii_lowercase used by upstream score_matches (NOT the
// Unicode-aware to_lowercase used by the BM25 tokenizer), so non-ASCII content
// scores identically to upstream.
func asciiLower(s string) string {
	b := []byte(s)
	changed := false
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(b)
}

func (c *SearchCompressor) scoreMatches(files []*fileMatches, context string) {
	contextLower := asciiLower(context)
	var contextWords []string
	for _, w := range strings.Fields(contextLower) {
		if len(w) > 2 {
			contextWords = append(contextWords, w)
		}
	}

	for _, fm := range files {
		for idx := range fm.matches {
			m := &fm.matches[idx]
			score := 0.0
			contentLower := asciiLower(m.content)
			for _, w := range contextWords {
				if strings.Contains(contentLower, w) {
					score += 0.3
				}
			}
			if c.config.boostErrors {
				sig := c.detector.Score(m.content, signals.Search)
				if sig.IsMatch() {
					switch *sig.Category {
					case signals.Error:
						score += 0.5
					case signals.Warning:
						score += 0.4
					case signals.Importance:
						score += 0.3
					case signals.Security, signals.Markdown:
						// +0.0
					}
				}
			}
			for _, kw := range c.config.contextKeywords {
				if strings.Contains(contentLower, asciiLower(kw)) {
					score += 0.4
				}
			}
			if score > 1.0 {
				score = 1.0
			}
			m.score = score
		}
	}
}

// selectMatches drops files beyond maxFiles (by total score desc), computes
// adaptiveTotal via adaptive.ComputeOptimalK, then per file (score-desc order)
// keeps first+last+top-scored up to the remaining budget, dedups via a seen set
// of (lineNo, fnv64(content)), and re-sorts each file's kept matches by line
// number ascending. Returns selected (sorted path order) and the scored-file
// count (files_affected).
func (c *SearchCompressor) selectMatches(files []*fileMatches, bias float64) (selected []*fileMatches, filesAffected int) {
	filesAffected = len(files)

	// (5a) order files by total score DESCENDING (stable for ties).
	byScore := make([]*fileMatches, len(files))
	copy(byScore, files)
	sort.SliceStable(byScore, func(a, b int) bool {
		return byScore[b].totalScore() < byScore[a].totalScore()
	})

	// (5b) file cap.
	if len(byScore) > c.config.maxFiles {
		byScore = byScore[:c.config.maxFiles]
	}

	// (5c) adaptive total from all surviving match strings.
	var allMatchStrings []string
	for _, fm := range byScore {
		for _, m := range fm.matches {
			allMatchStrings = append(allMatchStrings, fmt.Sprintf("%s:%d:%s", fm.file, m.lineNo, m.content))
		}
	}
	adaptiveTotal := adaptive.ComputeOptimalK(allMatchStrings, bias, 5, c.config.maxTotalMatches)

	// (5d) per-file fill in score-desc order.
	selectedByPath := make(map[string]*fileMatches)
	totalSelected := 0
	for _, fm := range byScore {
		if totalSelected >= adaptiveTotal {
			continue // global budget exhausted; skip whole file
		}

		// sorted copy by score DESC, tie-break line number ASC.
		sorted := make([]searchMatch, len(fm.matches))
		copy(sorted, fm.matches)
		sort.SliceStable(sorted, func(a, b int) bool {
			if sorted[a].score != sorted[b].score {
				return sorted[b].score < sorted[a].score
			}
			return sorted[a].lineNo < sorted[b].lineNo
		})

		// remaining_cap = maxMatchesPerFile.min(adaptiveTotal - totalSelected).
		remainingCap := c.config.maxMatchesPerFile
		budget := adaptiveTotal - totalSelected
		if budget < 0 {
			budget = 0
		}
		if budget < remainingCap {
			remainingCap = budget
		}

		seen := make(map[dedupKey]struct{})
		var fileSelected []searchMatch
		pushUnique := func(m searchMatch) {
			key := dedupKey{line: m.lineNo, hash: fnvHash(m.content)}
			if _, dup := seen[key]; dup {
				return
			}
			seen[key] = struct{}{}
			fileSelected = append(fileSelected, m)
		}

		if c.config.alwaysKeepFirst && len(fm.matches) > 0 {
			if len(fileSelected) < remainingCap {
				pushUnique(fm.matches[0])
			}
		}
		if c.config.alwaysKeepLast && len(fm.matches) > 1 {
			if len(fileSelected) < remainingCap {
				pushUnique(fm.matches[len(fm.matches)-1])
			}
		}
		for _, m := range sorted {
			if len(fileSelected) >= remainingCap {
				break
			}
			pushUnique(m)
		}

		// finalize: restore ascending line order.
		sort.SliceStable(fileSelected, func(a, b int) bool {
			return fileSelected[a].lineNo < fileSelected[b].lineNo
		})
		totalSelected += len(fileSelected)
		selectedByPath[fm.file] = &fileMatches{file: fm.file, matches: fileSelected}
	}

	// emit in sorted path order.
	paths := make([]string, 0, len(selectedByPath))
	for p := range selectedByPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		selected = append(selected, selectedByPath[p])
	}
	return selected, filesAffected
}

// dedupKey is the per-file dedup key: (line number, fnv64(content)).
type dedupKey struct {
	line uint64
	hash uint64
}

// fnvHash returns a deterministic 64-bit FNV-1a hash of s (used only for in-call
// dedup; exact values never leak into output, so any deterministic hash is
// behavior-equivalent to upstream's per-call DefaultHasher).
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// formatOutput renders each selected match as "<file>:<line>:<content>" (sorted
// path order), appending "[... and <n> more matches in <file>]" per file where
// matches were dropped relative to the original. Joins all lines with '\n'.
func (c *SearchCompressor) formatOutput(selected, original []*fileMatches) string {
	origByPath := make(map[string]*fileMatches, len(original))
	for _, fm := range original {
		origByPath[fm.file] = fm
	}

	var lines []string
	for _, fm := range selected {
		for _, m := range fm.matches {
			lines = append(lines, fmt.Sprintf("%s:%d:%s", m.file, m.lineNo, m.content))
		}
		if orig, ok := origByPath[fm.file]; ok && len(orig.matches) > len(fm.matches) {
			omitted := len(orig.matches) - len(fm.matches)
			lines = append(lines, fmt.Sprintf("[... and %d more matches in %s]", omitted, fm.file))
		}
	}
	return strings.Join(lines, "\n")
}
