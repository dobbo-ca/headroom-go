package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// readVerbs print raw file content. Ported from upstream's _READ_VERBS.
var readVerbs = map[string]bool{
	"cat": true, "head": true, "tail": true, "nl": true,
	"bat": true, "less": true, "more": true,
}

// shellWrappers prefix the real program and are peeled to find it. This is
// shell grammar, not tunable policy — `sudo cat f`, `timeout 30 cat f` and
// `rtk cat f` are all reads. Upstream lists rtk explicitly, which matters here:
// this machine rewrites dev commands through it.
var shellWrappers = map[string]bool{
	"rtk": true, "sudo": true, "env": true, "time": true, "nice": true,
	"ionice": true, "nohup": true, "stdbuf": true, "command": true,
	"timeout": true, "xargs": true,
}

// lockfileRe names dependency lockfiles. They read as plain text, so the
// content gate would protect them, but a tool regenerates them and no agent
// ever byte-patches one — and they are the biggest, most repetitive read in a
// session. Match by NAME so they stay compressible.
var lockfileRe = regexp.MustCompile(`(?i)(^|[\s/])(` +
	`bun\.lock|bun\.lockb|package-lock\.json|npm-shrinkwrap\.json|yarn\.lock|` +
	`pnpm-lock\.yaml|uv\.lock|poetry\.lock|Pipfile\.lock|` +
	`Cargo\.lock|go\.sum|Gemfile\.lock|composer\.lock|flake\.lock|Package\.resolved|` +
	`gradle\.lockfile|packages\.lock\.json` +
	`)(\s|$)`)

// writeRe spots a redirect, tee or heredoc anywhere in the command. Any of
// them means the command WRITES a file (`cat > f <<EOF`), so it is not a read.
var writeRe = regexp.MustCompile(`(^|\s)(>>?|tee\b|<<)`)

// sedRangeRe matches `sed -n`, which range-PRINTS. A bare `sed` is a stream
// editor, not a read.
var sedRangeRe = regexp.MustCompile(`(^|\s)-n(\s|$)`)

// cdPrefixRe strips the `cd <dir> && ` chains agents prefix reads with.
var cdPrefixRe = regexp.MustCompile(`^\s*cd\s+[^&;|]+(&&|;)\s*`)

// IsReadCommand reports whether a shell command's output is essentially raw
// FILE CONTENT the agent will read or edit from.
//
// Such output must NOT be lossy-compressed. Upstream records the evidence:
// doing so was observed on SWE-bench and mini-swe-agent to make the agent
// RE-READ the same file (cat -> cat -A -> cat -n) to recover exact detail —
// turn inflation — and, when recovery failed, to resolve the task wrongly.
// Search, list and test output (grep/rg/ls/find/pytest) is derived, not raw
// content, and stays compressible.
func IsReadCommand(command string) bool {
	if command == "" {
		return false
	}
	c := command
	for {
		stripped := cdPrefixRe.ReplaceAllString(c, "")
		if stripped == c {
			break
		}
		c = stripped
	}
	if writeRe.MatchString(c) {
		return false
	}
	prog, rest := bashProgram(c)
	if prog == "" {
		return false
	}
	if (prog == "sh" || prog == "bash" || prog == "zsh" || prog == "dash") && len(rest) > 0 {
		// `bash -lc "cat …"`: the real command is the -c argument.
		for j, tok := range rest {
			switch tok {
			case "-c", "-lc", "-lic", "-ic":
				if j+1 < len(rest) {
					return IsReadCommand(strings.Trim(strings.Join(rest[j+1:], " "), `'"`))
				}
			}
		}
		return false
	}
	isRead := readVerbs[prog] || (prog == "sed" && sedRangeRe.MatchString(c))
	if !isRead {
		return false
	}
	return !lockfileRe.MatchString(c)
}

// bashProgram returns the real program's base name and the tokens after it,
// peeling shell wrappers and leading VAR=value assignments.
func bashProgram(command string) (string, []string) {
	fields := strings.Fields(command)
	i := 0
	for i < len(fields) {
		tok := fields[i]
		// A leading VAR=value assignment is not the program.
		if strings.Contains(tok, "=") && !strings.HasPrefix(tok, "-") {
			i++
			continue
		}
		base := tok
		if slash := strings.LastIndex(base, "/"); slash >= 0 {
			base = base[slash+1:]
		}
		base = strings.ToLower(base)
		if shellWrappers[base] {
			i++
			// Skip the wrapper's own option and numeric arguments
			// (`timeout 30`, `nice -n 5`), or the next token would be
			// mistaken for the program.
			for i < len(fields) && (strings.HasPrefix(fields[i], "-") || isNumeric(fields[i])) {
				i++
			}
			continue
		}
		return base, fields[i+1:]
	}
	return "", nil
}

// isNumeric reports whether s is a bare number, e.g. the 30 in `timeout 30`.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return true
}

// releasableReadTypes are the content types safe to compress even when read
// from a file: confidently non-code, machine-derived DATA the agent never
// byte-patches.
//
// PlainText is deliberately NOT in this set. The code detector recognises only
// a handful of languages, so Ruby, C, SQL, shell and Markdown all fall through
// to PlainText — protecting PlainText is what keeps those reads safe. Upstream
// is explicit that the gate must not rely on positively identifying code; it
// releases only positively-identified data.
var releasableReadTypes = map[transform.ContentType]bool{
	transform.JsonArray:     true,
	transform.SearchResults: true,
	transform.BuildOutput:   true,
	transform.GitDiff:       true,
	transform.Html:          true,
}

// ReadOutputIsProtected reports whether a tool_result must be kept byte-exact.
//
// toolName is the producing tool ("Read", "Bash", …), command is the shell
// command for Bash (or ""), and filePath is the file_path from tool_input (or "").
// Protection is decided in two steps, both upstream's: is this a file read at all,
// and if so is the CONTENT a data type that is never byte-patched.
//
// The detector cannot see through Read's line-number prefixes: measured 2026-08-26,
// 0 of 53 MB of Read output classifies SourceCode, and 10.31 MB of source read via
// Read classified BuildOutput and was shredded ~80% by log_offload. The EXTENSION
// is unambiguous where the content heuristics are not, so it overrides the
// releasable set.
func ReadOutputIsProtected(toolName, command, filePath string, contentType transform.ContentType) bool {
	isRead := false
	switch toolName {
	case "Read", "read_file", "view":
		isRead = true
	case "Bash", "bash", "shell", "run_command":
		isRead = IsReadCommand(command)
	}
	if !isRead {
		return false
	}
	// Code file reads are protected regardless of detected content type.
	if codeExt := codeExtensionFromPath(filePath); codeExt != "" {
		return true
	}
	return !releasableReadTypes[contentType]
}

// codeExtensionFromPath returns the lowercase file extension if it's in the
// code allowlist, or "" otherwise. Kept as a separate function so the offloads
// package can export its own CodeExtension without a dependency cycle.
func codeExtensionFromPath(path string) string {
	// Inline check to avoid importing offloads (import cycle).
	// This is the same allowlist as offloads.codeExtensions.
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".java", ".rb", ".c", ".h", ".cpp", ".cc", ".hpp":
		return ext
	}
	return ""
}
