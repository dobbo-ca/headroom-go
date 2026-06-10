package offloads

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// DiffNoise is a self-contained OffloadTransform that drops lockfile and
// whitespace-only hunks from a git diff, replacing each dropped hunk body with a
// cell marker and stashing the whole original under a CCR key. It does not wrap a
// compressor.
type DiffNoise struct {
	minLines                int
	dropWhitespaceOnlyHunks bool
	lockfileSuffixes        []string
}

const diffNoiseConfidence = 0.9

// diffNoiseLockfileSuffixes is the EXACT 9-entry default list (ordered,
// case-sensitive) from upstream's embedded pipeline.toml.
var diffNoiseLockfileSuffixes = []string{
	"Cargo.lock",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"poetry.lock",
	"Pipfile.lock",
	"Gemfile.lock",
	"go.sum",
	"composer.lock",
}

// NewDiffNoise builds a DiffNoise with the upstream defaults: minLines=30,
// dropWhitespaceOnlyHunks=true, and the 9-entry lockfile suffix list.
func NewDiffNoise() *DiffNoise {
	return &DiffNoise{
		minLines:                30,
		dropWhitespaceOnlyHunks: true,
		lockfileSuffixes:        diffNoiseLockfileSuffixes,
	}
}

func (*DiffNoise) Name() string { return "diff_noise" }

func (*DiffNoise) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.GitDiff}
}

func (*DiffNoise) Confidence() float32 { return diffNoiseConfidence }

// diffSegment is one file's worth of a diff: the header lines (diff --git up to
// but not including the first @@) and the body lines (from the first @@ onward).
type diffSegment struct {
	newPath     string
	headerLines []string
	bodyLines   []string
}

// EstimateBloat measures the fraction of body bytes that are droppable (lockfile
// or whitespace-only). Returns 0 on empty input or fewer than minLines lines.
func (o *DiffNoise) EstimateBloat(content string) float32 {
	if content == "" {
		return 0
	}
	if len(splitLinesRust(content)) < o.minLines {
		return 0
	}
	segments := o.parseSegments(content)
	if len(segments) == 0 {
		return 0
	}
	totalBytes := 0
	droppableBytes := 0
	for _, seg := range segments {
		bodyBytes := 0
		for _, line := range seg.bodyLines {
			bodyBytes += len(line) + 1
		}
		totalBytes += bodyBytes
		droppable := o.isLockfile(seg.newPath) ||
			(o.dropWhitespaceOnlyHunks && bodyIsWhitespaceOnly(seg.bodyLines))
		if droppable {
			droppableBytes += bodyBytes
		}
	}
	if totalBytes == 0 {
		return 0
	}
	return clamp01(float32(droppableBytes) / float32(totalBytes))
}

// Apply emits each segment's header verbatim, replaces droppable hunk bodies with
// a cell marker, and stashes the original under a CCR key. Skips when there are no
// diff sections, nothing was droppable, or the output would not be smaller.
func (o *DiffNoise) Apply(content string, _ transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	segments := o.parseSegments(content)
	if len(segments) == 0 {
		return transform.OffloadOutput{}, fmt.Errorf("diff_noise: no diff sections: %w", transform.ErrSkipped)
	}

	var b strings.Builder
	b.Grow(len(content))
	droppedAny := false
	for _, seg := range segments {
		for _, h := range seg.headerLines {
			b.WriteString(h)
			b.WriteByte('\n')
		}
		dropLockfile := o.isLockfile(seg.newPath)
		dropWhitespace := o.dropWhitespaceOnlyHunks && bodyIsWhitespaceOnly(seg.bodyLines)
		if dropLockfile || dropWhitespace {
			reason := "whitespace-only"
			if dropLockfile {
				reason = "lockfile"
			}
			b.WriteString("[diff_noise: ")
			b.WriteString(reason)
			b.WriteString(" hunks dropped (")
			b.WriteString(strconv.Itoa(len(seg.bodyLines)))
			b.WriteString(" lines)]\n")
			droppedAny = true
		} else {
			for _, line := range seg.bodyLines {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}

	output := leadingPreDiffLines(content) + b.String()

	if !droppedAny || len(output) >= len(content) {
		return transform.OffloadOutput{}, fmt.Errorf("diff_noise: no droppable hunks: %w", transform.ErrSkipped)
	}

	key := ccr.ComputeKeyMD5([]byte(content))
	store.Put(key, content)
	output += "\n[diff_noise CCR: hash=" + key + "]"
	return fromLengths(len(content), output, key), nil
}

// parseSegments walks the diff. A new segment begins on each "diff --git" line;
// header lines accumulate until the first "@@", after which (and including it)
// lines go into the body. Pre-diff prelude lines (before the first "diff --git")
// are skipped here — they are re-attached by leadingPreDiffLines.
func (o *DiffNoise) parseSegments(content string) []diffSegment {
	var segments []diffSegment
	var cur *diffSegment
	inBody := false
	for _, line := range splitLinesRust(content) {
		if strings.HasPrefix(line, "diff --git") {
			if cur != nil {
				segments = append(segments, *cur)
			}
			cur = &diffSegment{
				newPath:     parseNewPath(line),
				headerLines: []string{line},
			}
			inBody = false
			continue
		}
		if cur == nil {
			continue
		}
		if !inBody {
			if strings.HasPrefix(line, "@@") {
				inBody = true
				cur.bodyLines = append(cur.bodyLines, line)
			} else {
				cur.headerLines = append(cur.headerLines, line)
			}
			continue
		}
		cur.bodyLines = append(cur.bodyLines, line)
	}
	if cur != nil {
		segments = append(segments, *cur)
	}
	return segments
}

// parseNewPath returns everything after the last " b/" in a header line, or "".
func parseNewPath(header string) string {
	idx := strings.LastIndex(header, " b/")
	if idx < 0 {
		return ""
	}
	return header[idx+3:]
}

// leadingPreDiffLines re-attaches every line (with '\n') before the first
// "diff --git" line, preserving any git format-patch prelude verbatim.
func leadingPreDiffLines(content string) string {
	var b strings.Builder
	for _, line := range splitLinesRust(content) {
		if strings.HasPrefix(line, "diff --git") {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// isLockfile reports whether path ends with one of the lockfile suffixes at a
// path-segment boundary (start-of-path, or right after '/' or '\\'). So
// "crates/foo/Cargo.lock" and bare "Cargo.lock" match, but "MyCargo.lock" does not.
func (o *DiffNoise) isLockfile(path string) bool {
	if path == "" {
		return false
	}
	for _, suffix := range o.lockfileSuffixes {
		if !strings.HasSuffix(path, suffix) {
			continue
		}
		prefixLen := len(path) - len(suffix)
		if prefixLen == 0 {
			return true
		}
		if b := path[prefixLen-1]; b == '/' || b == '\\' {
			return true
		}
	}
	return false
}

// bodyIsWhitespaceOnly reports whether a hunk body's additions and deletions are
// equal after stripping ASCII whitespace. It requires at least one change line
// (a pure-context hunk is NOT whitespace-only) and compares the add/sub bodies
// order-aware, element for element. File-header lines (+++/---) are excluded.
func bodyIsWhitespaceOnly(bodyLines []string) bool {
	var adds, subs []string
	sawChange := false
	for _, line := range bodyLines {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			sawChange = true
			adds = append(adds, stripWS(line[1:]))
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			sawChange = true
			subs = append(subs, stripWS(line[1:]))
		}
	}
	if !sawChange {
		return false
	}
	if len(adds) != len(subs) {
		return false
	}
	for i := range adds {
		if adds[i] != subs[i] {
			return false
		}
	}
	return true
}

// stripWS removes every ASCII whitespace byte (space, \t, \n, \r, \v, \f) from s.
// It does NOT strip Unicode whitespace, matching Rust's is_ascii_whitespace.
func stripWS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			// skip
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
