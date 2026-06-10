package compress

import "strings"

// splitLinesRust splits s into lines with Rust str::lines() semantics: split on
// '\n', strip one trailing '\r' per line (so "\r\n" line endings lose the '\r'),
// and drop the single trailing empty element when s ends with '\n'. An empty
// string yields an empty slice (no lines), matching str::lines().
//
// The log compressor uses these semantics for its line split (mirroring
// upstream content.lines()). diff/search compressors instead use
// strings.Split(content, "\n") (Python str.split semantics) and must NOT use
// this helper.
func splitLinesRust(s string) []string {
	if s == "" {
		return nil
	}
	endsWithNewline := strings.HasSuffix(s, "\n")
	parts := strings.Split(s, "\n")
	if endsWithNewline {
		// Drop the trailing empty element produced when s ends with '\n'.
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		// Rust str::lines() strips '\r' only as part of a '\r\n' terminator. Every
		// part except the final one (when s does not end with '\n') was
		// '\n'-terminated; a lone trailing '\r' on the unterminated last line is
		// preserved.
		if i == len(parts)-1 && !endsWithNewline {
			continue
		}
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}
