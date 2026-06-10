// Package tagprotect protects custom (non-HTML5) tag regions from a downstream
// compressor by swapping them for opaque "{{HEADROOM_TAG_N}}" placeholders, and
// restores them afterwards. It is a pre/post wrapper around the compression
// pipeline — NOT a CCR/offload Transform (no AppliesTo/EstimateBloat/Confidence,
// no CCR marker). It is a faithful port of upstream headroom's tag_protector.
//
// Determinism (I4): the collision-avoidance prefix is chosen by a deterministic
// salt scan (no crypto/rand, no time).
package tagprotect

import (
	"log/slog"
	"strconv"
	"strings"
)

const (
	// defaultPrefix is the default placeholder prefix and the literal probed for
	// collision detection.
	defaultPrefix = "{{HEADROOM_TAG_"
	// placeholderSuffix is appended after the counter.
	placeholderSuffix = "}}"
	// fallbackPrefix is used when all 16 salted prefixes still collide.
	fallbackPrefix = "{{HEADROOM_TAG_FALLBACK_a4f1c7e2_"
	// saltAttempts is the number of salted prefix candidates tried.
	saltAttempts = 16
)

// ProtectStats reports what the protect pass observed.
type ProtectStats struct {
	TagsSeen                    int
	HTMLTagsSkipped             int
	CustomBlocksProtected       int
	SelfClosingProtected        int
	OrphanCloses                int
	PlaceholderCollisionAvoided bool
}

// spanKind identifies what a protected span represents.
type spanKind int

const (
	kindBlock spanKind = iota
	kindSelfClosing
	kindOpenMarker
	kindCloseMarker
)

// span is a half-open byte range [start,end) of text to be replaced.
type span struct {
	start int
	end   int
	kind  spanKind
}

// openTag is a stack entry for an unmatched custom open tag.
type openTag struct {
	nameLower string
	openStart int
}

// tagKind classifies a parse_tag_at result.
type tagKind int

const (
	notTag tagKind = iota
	openTagKind
	closeTagKind
)

// parsedTag is the result of lexing a '<...>' construct.
type parsedTag struct {
	kind          tagKind
	nameStart     int // byte index of the first name char
	nameEnd       int // byte index just past the name
	tagEnd        int // byte index just past the closing '>'
	isSelfClosing bool
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIIAlnum(b byte) bool {
	return isASCIILetter(b) || (b >= '0' && b <= '9')
}

func isNameStart(b byte) bool {
	return isASCIILetter(b) || b == '_'
}

func isNameCont(b byte) bool {
	return isASCIIAlnum(b) || b == '_' || b == '-' || b == '.' || b == ':'
}

func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

// parseTagAt lexes the construct at bytes[start] (which must be '<').
func parseTagAt(bytes []byte, start int) parsedTag {
	n := len(bytes)
	j := start + 1
	isClose := false
	if j < n && bytes[j] == '/' {
		isClose = true
		j++
	}
	// Require a name-start char.
	if j >= n || !isNameStart(bytes[j]) {
		return parsedTag{kind: notTag}
	}
	nameStart := j
	for j < n && isNameCont(bytes[j]) {
		j++
	}
	nameEnd := j

	if isClose {
		// Close tag: skip whitespace, require '>'.
		for j < n && isASCIIWhitespace(bytes[j]) {
			j++
		}
		if j < n && bytes[j] == '>' {
			return parsedTag{kind: closeTagKind, nameStart: nameStart, nameEnd: nameEnd, tagEnd: j + 1}
		}
		return parsedTag{kind: notTag}
	}

	// Open tag: lex attributes honoring quotes; detect "/>".
	for j < n {
		b := bytes[j]
		switch b {
		case '"', '\'':
			quote := b
			j++
			for j < n && bytes[j] != quote {
				j++
			}
			if j >= n {
				// EOF inside a quote => NotTag.
				return parsedTag{kind: notTag}
			}
			j++ // consume closing quote
		case '/':
			if j+1 < n && bytes[j+1] == '>' {
				return parsedTag{kind: openTagKind, nameStart: nameStart, nameEnd: nameEnd, tagEnd: j + 2, isSelfClosing: true}
			}
			j++
		case '>':
			return parsedTag{kind: openTagKind, nameStart: nameStart, nameEnd: nameEnd, tagEnd: j + 1}
		default:
			j++
		}
	}
	// EOF before '>' => NotTag.
	return parsedTag{kind: notTag}
}

// choosePrefix picks the placeholder prefix, avoiding collisions deterministically.
func choosePrefix(text string) (prefix string, collisionAvoided bool) {
	if !strings.Contains(text, defaultPrefix) {
		return defaultPrefix, false
	}
	for salt := 0; salt < saltAttempts; salt++ {
		candidate := "{{HEADROOM_TAG_" + strconv.Itoa(salt) + "_"
		if !strings.Contains(text, candidate) {
			return candidate, true
		}
	}
	return fallbackPrefix, true
}

// ProtectTags swaps custom (non-HTML5) tag regions for opaque placeholders.
// compressTaggedContent=false protects whole <tag>...</tag> blocks (content
// hidden from the compressor); true protects only the open/close markers so the
// inner content flows through. Returns (protected, blocks, stats) where blocks
// is an ordered slice of (placeholder, original) pairs.
func ProtectTags(text string, compressTaggedContent bool) (string, [][2]string, ProtectStats) {
	var stats ProtectStats

	// STAGE 0 — early-return guard.
	if text == "" || !strings.Contains(text, "<") {
		return text, nil, stats
	}

	// STAGE 0b — collision detection / prefix selection.
	prefix, collisionAvoided := choosePrefix(text)
	stats.PlaceholderCollisionAvoided = collisionAvoided

	// STAGE 1 — single-pass byte walk to collect spans.
	bytes := []byte(text)
	n := len(bytes)
	var spans []span
	var stack []openTag

	i := 0
	for i < n {
		if bytes[i] != '<' {
			i++
			continue
		}
		tag := parseTagAt(bytes, i)
		if tag.kind == notTag {
			i++
			continue
		}

		nameLower := strings.ToLower(string(bytes[tag.nameStart:tag.nameEnd]))
		stats.TagsSeen++

		if IsKnownHTMLTag(nameLower) {
			// HTML5 tag: emit verbatim, never protect.
			stats.HTMLTagsSkipped++
			i = tag.tagEnd
			continue
		}

		switch tag.kind {
		case openTagKind:
			if tag.isSelfClosing {
				// Custom self-closing: protect immediately in both modes.
				spans = append(spans, span{start: i, end: tag.tagEnd, kind: kindSelfClosing})
				stats.SelfClosingProtected++
			} else if compressTaggedContent {
				// Marker-only mode: push an OpenMarker span and the stack entry.
				spans = append(spans, span{start: i, end: tag.tagEnd, kind: kindOpenMarker})
				stack = append(stack, openTag{nameLower: nameLower, openStart: i})
			} else {
				// Block mode: stack only, no span yet.
				stack = append(stack, openTag{nameLower: nameLower, openStart: i})
			}
		case closeTagKind:
			// Search the stack from TOP for a matching name.
			matchIdx := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].nameLower == nameLower {
					matchIdx = k
					break
				}
			}
			if matchIdx < 0 {
				// Orphan close: emit verbatim.
				stats.OrphanCloses++
				i = tag.tagEnd
				continue
			}
			matched := stack[matchIdx]
			// Truncate the stack to matchIdx then pop the matched entry.
			stack = stack[:matchIdx]
			if compressTaggedContent {
				// Marker-only mode: emit a separate CloseMarker span.
				spans = append(spans, span{start: i, end: tag.tagEnd, kind: kindCloseMarker})
			} else {
				// Block mode: remove inner spans subsumed by this block, then
				// emit one outer Block span.
				kept := spans[:0]
				for _, s := range spans {
					if s.start < matched.openStart {
						kept = append(kept, s)
					}
				}
				spans = kept
				spans = append(spans, span{start: matched.openStart, end: tag.tagEnd, kind: kindBlock})
				stats.CustomBlocksProtected++
			}
		}
		i = tag.tagEnd
	}

	// STAGE 2 — emit via offset slicing (spans are sorted & non-overlapping).
	if len(spans) == 0 {
		return text, nil, stats
	}
	var out strings.Builder
	blocks := make([][2]string, 0, len(spans))
	cursor := 0
	for counter, s := range spans {
		out.WriteString(text[cursor:s.start])
		placeholder := prefix + strconv.Itoa(counter) + placeholderSuffix
		blocks = append(blocks, [2]string{placeholder, text[s.start:s.end]})
		out.WriteString(placeholder)
		cursor = s.end
	}
	out.WriteString(text[cursor:])

	return out.String(), blocks, stats
}

// RestoreTags is the inverse of ProtectTags. It forward-iterates blocks and
// replaces all occurrences of each placeholder with its original. A missing
// placeholder is logged at ERROR level and skipped (the original is NOT
// injected) — Hotfix-A9.
func RestoreTags(text string, blocks [][2]string) string {
	return RestoreTagsWithRequestID(text, blocks, "")
}

// RestoreTagsWithRequestID is RestoreTags with an optional request_id threaded
// into the structured error log when a placeholder is missing.
func RestoreTagsWithRequestID(text string, blocks [][2]string, requestID string) string {
	working := text
	for _, b := range blocks {
		placeholder, original := b[0], b[1]
		if strings.Contains(working, placeholder) {
			working = strings.ReplaceAll(working, placeholder, original)
			continue
		}
		// Missing placeholder: log and skip (do not inject the original).
		if requestID != "" {
			slog.Error("tagprotect: placeholder missing during restore",
				"placeholder", placeholder, "request_id", requestID)
		} else {
			slog.Error("tagprotect: placeholder missing during restore",
				"placeholder", placeholder)
		}
	}
	return working
}
