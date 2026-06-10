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
	parts := strings.Split(s, "\n")
	// Drop the trailing empty element produced when s ends with '\n'.
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}
