package offloads

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
	"github.com/tidwall/gjson"
)

const (
	readOutlineConfidence = 0.9
	// elisionMarker is the comment inserted where function bodies are removed.
	// Unnumbered (no line-number prefix) so it's visibly not file content.
	elisionMarker = "// ... (body elided by Headroom; Read a specific line range to see it)\n"
)

// rangeKeys are tool_input keys that indicate the model targeted a specific
// line range; outlining would frustrate that intent and likely cause a re-read.
// Ported from upstream's _RANGE_KEYS (headroom/proxy/interceptors/astgrep.py:51).
var rangeKeys = []string{"offset", "limit", "line_range", "start_line", "end_line", "ranges"}

// codeExtensions is the allowlist of file extensions for which outlining is
// supported. Ported from upstream's _EXT_TO_LANG keys. Go-only in v0, but the
// allowlist structure is preserved for future expansion and so the gate can
// reuse it.
var codeExtensions = map[string]bool{
	".go":   true,
	".py":   true,
	".ts":   true,
	".tsx":  true,
	".js":   true,
	".jsx":  true,
	".rs":   true,
	".java": true,
	".rb":   true,
	".c":    true,
	".h":    true,
	".cpp":  true,
	".cc":   true,
	".hpp":  true,
}

// CodeExtension returns the lowercase file extension if it's in the code
// allowlist, or "" otherwise. Shared helper for both the outline and the gate.
func CodeExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if codeExtensions[ext] {
		return ext
	}
	return ""
}

// ReadOutline is an OffloadTransform that outlines verbose code-file Read
// outputs by eliding function bodies and keeping signatures, doc comments, and
// line numbers intact.
//
// PR #21 landed TextCrusher WITH a read-protection gate because lossy-compressing
// raw file content was observed upstream (SWE-bench, mini-swe-agent) to make the
// agent re-read the same file to recover detail — turn inflation — and, when
// recovery failed, to resolve the task wrongly. Sentence-dropping on source code
// is unsafe because it deletes arbitrary bytes: the agent needs exact bytes to
// produce a precise patch, and the crusher cannot tell a signature from a comment
// from a loop body.
//
// An outline is a different bargain because the unit of loss is a syntactic body,
// chosen by a parser, not a similarity score. Every package clause, every import,
// every type and const declaration, every doc comment, every function signature
// and every closing brace survives — with its original line number. The only bytes
// removed are the lines strictly between a FuncDecl's braces, and each removal is
// replaced by a marker that names the transform and the recovery action.
//
// Three guards make it safe, and it is unsafe without any one of them:
//  1. The range veto. A Read carrying offset/limit/line_range/start_line/end_line/ranges
//     is never touched. That is the model saying it already knows which lines it wants.
//  2. Second read returns raw. A file already Read anywhere earlier in this body,
//     frozen prefix included, passes through. The model came back for more; give it more.
//  3. Two recovery paths, both in-band. Line numbers are preserved so Read(file, offset=N)
//     lands exactly on the elided body; and the block carries <<ccr:HASH>> so
//     headroom_retrieve returns the original bytes verbatim without a second file read.
type ReadOutline struct{}

// NewReadOutline constructs a ReadOutline.
func NewReadOutline() *ReadOutline {
	return &ReadOutline{}
}

func (*ReadOutline) Name() string { return "read_outline" }

func (*ReadOutline) AppliesTo() []transform.ContentType {
	// Register for all text-like types so the outline runs FIRST in the pipeline.
	// The detector cannot see through Read's line-number prefixes: measured 2026-08-26,
	// 0 of 53 MB of Read output classifies SourceCode. So we register broadly and
	// decline explicitly on non-Go extensions in Apply.
	return []transform.ContentType{
		transform.PlainText,
		transform.BuildOutput,
		transform.SearchResults,
		transform.SourceCode,
	}
}

func (*ReadOutline) Confidence() float32 { return readOutlineConfidence }

// EstimateBloat reports how much of the content looks droppable. For Go source
// with function bodies, we estimate 50% bloat (bodies vs signatures). Short
// content or content without "func " gets 0.
func (*ReadOutline) EstimateBloat(content string) float32 {
	if len(content) < 512 {
		return 0
	}
	if strings.Contains(content, "func ") {
		return 0.5
	}
	return 0
}

// Apply outlines Go source code by eliding function bodies and keeping signatures.
// Declines (ErrSkipped) when: range keys present, prior read, no path, non-Go
// extension, parse error, or no elidable bodies.
func (o *ReadOutline) Apply(content string, ctx transform.CompressionContext, store ccr.Store) (transform.OffloadOutput, error) {
	// 1. PATH CHECK. Extract file_path from ToolInput. Fail closed: empty
	// ToolInput or no path key → no_path.
	path := filePathFromToolInput(ctx.ToolInput)
	if path == "" {
		return transform.OffloadOutput{}, fmt.Errorf("read_outline: no_path: %w", transform.ErrSkipped)
	}

	// 2. EXTENSION CHECK. Go-only in v0.
	ext := CodeExtension(path)
	if ext != ".go" {
		return transform.OffloadOutput{}, fmt.Errorf("read_outline: not_go (ext=%s): %w", ext, transform.ErrSkipped)
	}

	// 3. RANGE VETO. Respect explicit line ranges — the model wants those
	// specific lines.
	for _, key := range rangeKeys {
		if gjson.Get(ctx.ToolInput, key).Exists() {
			return transform.OffloadOutput{}, fmt.Errorf("read_outline: range_key (%s): %w", key, transform.ErrSkipped)
		}
	}

	// 4. PROGRESSIVE DISCLOSURE. Second read of the same file returns raw.
	// PriorReads is computed by blockContext from the toolpairs.Index.
	if ctx.PriorReads > 0 {
		return transform.OffloadOutput{}, fmt.Errorf("read_outline: prior_read (count=%d): %w", ctx.PriorReads, transform.ErrSkipped)
	}

	// 5. OUTLINE. Strip line-number prefixes, parse, elide bodies, emit with
	// prefixes intact.
	outlined, err := outlineGo(content)
	if err != nil {
		return transform.OffloadOutput{}, fmt.Errorf("read_outline: %w: %w", err, transform.ErrSkipped)
	}

	// 6. STORE. Put the original under MD5 key (matching TextOffload).
	key := ccr.ComputeKeyMD5([]byte(content))
	store.Put(key, content)

	return fromLengths(len(content), outlined, key), nil
}

// outlineGo parses Go source and elides function bodies, keeping signatures and
// line numbers intact.
func outlineGo(content string) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("parse_error: empty content")
	}

	// Strip line-number prefixes. Claude Code prefixes Read output with "N\t"
	// where N can have leading spaces for alignment. The fixture shows lines like
	// "     1\tpackage example", "     6\tfunc QuantizeIndexed", etc.
	// Blank lines in numbered output have the number but no tab: "     2\n".
	// Check if at least 40% of lines have a tab to decide it's numbered.
	hasTab := 0
	for _, line := range lines {
		if strings.Contains(line, "\t") {
			hasTab++
		}
	}

	var bare []string
	isNumbered := len(lines) > 0 && hasTab >= len(lines)*4/10
	if isNumbered {
		for _, line := range lines {
			// Strip the prefix: everything up to and including the first tab.
			if idx := strings.IndexByte(line, '\t'); idx >= 0 {
				bare = append(bare, line[idx+1:])
			} else {
				// Line without a tab: strip leading spaces and digits, keep the rest.
				// This handles blank lines like "     2\n" → "\n" → ""
				trimmed := strings.TrimLeft(line, " \t0123456789")
				bare = append(bare, trimmed)
			}
		}
	} else {
		bare = lines
	}

	// Parse the bare source.
	src := strings.Join(bare, "\n")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "source.go", src, parser.SkipObjectResolution)
	if err != nil || f == nil {
		return "", fmt.Errorf("parse_error: %v", err)
	}

	// Collect lines to drop: for each FuncDecl with a non-nil Body, drop the
	// lines strictly between Lbrace and Rbrace (lo+1 to hi-1 inclusive).
	drop := make(map[int]bool)
	bodyCount := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		lo := fset.Position(fn.Body.Lbrace).Line
		hi := fset.Position(fn.Body.Rbrace).Line
		if hi-lo < 2 {
			// Nothing strictly inside (one-line body or same-line braces).
			continue
		}
		bodyCount++
		// Drop lines (lo+1) to (hi-1) inclusive (1-indexed).
		for i := lo + 1; i < hi; i++ {
			drop[i] = true
		}
	}

	if bodyCount == 0 {
		return "", fmt.Errorf("no_bodies: no elidable function bodies")
	}

	// Emit the output: original lines (prefixes intact), inserting the marker
	// after each body's opening brace.
	var out strings.Builder
	for i, line := range lines {
		lineNum := i + 1 // 1-indexed to match go/token.Position
		if drop[lineNum] {
			// First dropped line after an opening brace: emit the marker once.
			// Check if the previous line was NOT dropped (i.e., it's the brace line).
			if i == 0 || !drop[lineNum-1] {
				out.WriteString(elisionMarker)
			}
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}

	// Append the trailing summary line (matching TextOffload shape).
	out.WriteString(fmt.Sprintf("[headroom: %d function bodies elided. Re-read with offset/limit for a specific body, or retrieve the full file: hash=%s]\n",
		bodyCount, ccr.ComputeKeyMD5([]byte(content))))

	return out.String(), nil
}
