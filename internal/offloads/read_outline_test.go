package offloads

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/read_outline/" + name)
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return string(b)
}

// expectSkipped asserts that Apply returned ErrSkipped with the given reason substring.
func expectSkipped(t *testing.T, err error, reason string) {
	t.Helper()
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("expected ErrSkipped, got %v", err)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("expected error containing %q, got %v", reason, err)
	}
}

// 1. TestRangeKeyVetoLeavesReadUntouched — table over all six keys.
func TestRangeKeyVetoLeavesReadUntouched(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	rangeKeys := []string{"offset", "limit", "line_range", "start_line", "end_line", "ranges"}

	st := store(t)
	ro := NewReadOutline()

	for _, key := range rangeKeys {
		t.Run(key, func(t *testing.T) {
			// Tool input WITH the range key
			inputWith := fmt.Sprintf(`{"file_path":"example.go","%s":10}`, key)
			ctx := transform.CompressionContext{
				ProducingTool: "Read",
				ToolInput:     inputWith,
			}
			_, err := ro.Apply(fixture, ctx, st)
			expectSkipped(t, err, "range_key")

			// WITHOUT the range key, same content must outline
			inputWithout := `{"file_path":"example.go"}`
			ctxOK := transform.CompressionContext{
				ProducingTool: "Read",
				ToolInput:     inputWithout,
			}
			out, err := ro.Apply(fixture, ctxOK, st)
			if err != nil {
				t.Fatalf("without %s, expected success, got %v", key, err)
			}
			if len(out.Output) >= len(fixture) {
				t.Fatalf("without %s, outline should be shorter", key)
			}
		})
	}
}

// 2. TestSecondReadOfSameFileReturnsRaw — PriorReads > 0 declines.
func TestSecondReadOfSameFileReturnsRaw(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"foo.go"}`

	st := store(t)
	ro := NewReadOutline()

	// First read: PriorReads=0, should outline
	ctx1 := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
		PriorReads:    0,
	}
	out1, err := ro.Apply(fixture, ctx1, st)
	if err != nil {
		t.Fatalf("first read: expected success, got %v", err)
	}
	if len(out1.Output) >= len(fixture) {
		t.Fatalf("first read: outline should be shorter")
	}

	// Second read: PriorReads=1, should decline
	ctx2 := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
		PriorReads:    1,
	}
	_, err = ro.Apply(fixture, ctx2, st)
	expectSkipped(t, err, "prior_read")
}

// 3. TestFrozenPrefixCountsAsDisclosed — progressive disclosure from cache.
// When blockContext sees a Read in the frozen prefix (FrozenCount > 0), it
// increments PriorReads for any later Read of the same file_path. This test
// verifies that Apply declines when PriorReads=1, as it would if the file
// was already Read in the frozen prefix.
func TestFrozenPrefixCountsAsDisclosed(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"bar.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
		PriorReads:    1, // Simulates one prior read in frozen prefix
	}
	_, err := ro.Apply(fixture, ctx, st)
	expectSkipped(t, err, "prior_read")
}

// 4. TestNoToolInputDeclines — empty ToolInput declines at path check.
func TestNoToolInputDeclines(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     "", // Empty
	}
	_, err := ro.Apply(fixture, ctx, st)
	expectSkipped(t, err, "no_path")
}

// 5. TestNonGoExtensionDeclines — .py, .md, .txt, no extension.
func TestNonGoExtensionDeclines(t *testing.T) {
	pyFixture := loadFixture(t, "python.py.txt")
	st := store(t)
	ro := NewReadOutline()

	cases := []struct {
		path string
		desc string
	}{
		{`example.py`, "Python"},
		{`README.md`, "Markdown"},
		{`data.txt`, "Text"},
		{`Makefile`, "No extension"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			input := fmt.Sprintf(`{"file_path":%q}`, tc.path)
			ctx := transform.CompressionContext{
				ProducingTool: "Read",
				ToolInput:     input,
			}
			_, err := ro.Apply(pyFixture, ctx, st)
			expectSkipped(t, err, "not_go")
		})
	}
}

// 6. TestParseFailurePassesThrough — truncated Go.
func TestParseFailurePassesThrough(t *testing.T) {
	fixture := loadFixture(t, "truncated.go.txt")
	input := `{"file_path":"broken.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	_, err := ro.Apply(fixture, ctx, st)
	expectSkipped(t, err, "parse_error")
}

// 7. TestNoElidableBodiesDeclines — Go file with only types/consts.
func TestNoElidableBodiesDeclines(t *testing.T) {
	st := store(t)
	ro := NewReadOutline()

	t.Run("only_types_and_consts", func(t *testing.T) {
		fixture := loadFixture(t, "no_bodies.go.txt")
		input := `{"file_path":"constants.go"}`
		ctx := transform.CompressionContext{
			ProducingTool: "Read",
			ToolInput:     input,
		}
		_, err := ro.Apply(fixture, ctx, st)
		expectSkipped(t, err, "no_bodies")
	})

	t.Run("one_line_funcs", func(t *testing.T) {
		fixture := loadFixture(t, "oneline_funcs.go.txt")
		input := `{"file_path":"short.go"}`
		ctx := transform.CompressionContext{
			ProducingTool: "Read",
			ToolInput:     input,
		}
		_, err := ro.Apply(fixture, ctx, st)
		expectSkipped(t, err, "no_bodies")
	})
}

// 8. TestOutlineIsByteIdenticalAcrossRuns — determinism (I4).
func TestOutlineIsByteIdenticalAcrossRuns(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}

	var results []string
	for i := 0; i < 20; i++ {
		out, err := ro.Apply(fixture, ctx, st)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		results = append(results, out.Output)
	}

	// All runs must produce byte-identical output
	first := results[0]
	for i := 1; i < len(results); i++ {
		if results[i] != first {
			t.Fatalf("run %d differs from run 0", i)
		}
	}
}

// 9. TestOutlineKeepsEveryDefinitionAndItsLineNumber — value proposition.
func TestOutlineKeepsEveryDefinitionAndItsLineNumber(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	out, err := ro.Apply(fixture, ctx, st)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Expected signatures with their line numbers (from the fixture)
	expectedSigs := []string{
		"     6\tfunc QuantizeIndexed(img image.Image, numColors int) *image.Paletted {",
		"    21\tfunc extractColors(img image.Image, n int) []color.Color {",
		"    41\tfunc ConvertToGrayscale(img image.Image) *image.Gray {",
	}

	for _, sig := range expectedSigs {
		if !strings.Contains(out.Output, sig) {
			t.Errorf("outline missing signature: %q", sig)
		}
	}

	// Verify closing braces survived (M1 mutation)
	if !strings.Contains(out.Output, "    18\t}") {
		t.Errorf("outline missing closing brace at line 18")
	}
	if !strings.Contains(out.Output, "    38\t}") {
		t.Errorf("outline missing closing brace at line 38")
	}
	if !strings.Contains(out.Output, "    50\t}") {
		t.Errorf("outline missing closing brace at line 50")
	}
}

// 12. TestOutlineOriginalResolvesFromTheStore — CCR contract.
func TestOutlineOriginalResolvesFromTheStore(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	out, err := ro.Apply(fixture, ctx, st)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// CacheKey must resolve to the original
	if out.CacheKey == "" {
		t.Fatalf("CacheKey is empty")
	}
	got, ok := st.Get(out.CacheKey)
	if !ok {
		t.Fatalf("CacheKey %s does not resolve in store", out.CacheKey)
	}
	if got != fixture {
		t.Fatalf("CacheKey resolves to wrong content (len=%d, want %d)", len(got), len(fixture))
	}

	// Store length check
	if st.Len() == 0 {
		t.Fatalf("store is empty after outline")
	}
}

// 13. TestNoAstGrepBinaryIsIrrelevant — by construction, no ast-grep dependency.
func TestNoAstGrepBinaryIsIrrelevant(t *testing.T) {
	// This test is a documentation test: read_outline uses go/parser, not ast-grep.
	// The outline fires regardless of whether ast-grep exists on PATH.

	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	out, err := ro.Apply(fixture, ctx, st)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out.Output) >= len(fixture) {
		t.Fatalf("outline should be shorter")
	}

	// Verify no os/exec import exists in production code (compile-time check).
	// This is enforced by the implementation not importing os/exec.
}

// 14. TestNonReadToolDeclines — tool check (fixes F5/verifier B).
func TestNonReadToolDeclines(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	nonReadTools := []string{"Write", "Edit", "MultiEdit", "Bash", "mcp__fs__write_file", "str_replace_editor", ""}
	for _, tool := range nonReadTools {
		t.Run(tool, func(t *testing.T) {
			ctx := transform.CompressionContext{
				ProducingTool: tool,
				ToolInput:     input,
			}
			_, err := ro.Apply(fixture, ctx, st)
			expectSkipped(t, err, "not_read_tool")
		})
	}
}

// 15. TestElisionMarkerIsValidGo — M2 mutation.
func TestElisionMarkerIsValidGo(t *testing.T) {
	fixture := loadFixture(t, "three_funcs_numbered.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	out, err := ro.Apply(fixture, ctx, st)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Marker must be a valid Go comment
	if !strings.Contains(out.Output, "// ... (body elided by Headroom") {
		t.Fatalf("marker not found or invalid")
	}
	// Must not be a Python/shell comment
	if strings.Contains(out.Output, "# ... (body elided") {
		t.Fatalf("marker is not a Go comment")
	}
}

// 16. TestImportsStructConstInterfaceAllSurvive — M6 mutation.
func TestImportsStructConstInterfaceAllSurvive(t *testing.T) {
	fixture := loadFixture(t, "rich_declarations.txt")
	input := `{"file_path":"example.go"}`

	st := store(t)
	ro := NewReadOutline()

	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     input,
	}
	out, err := ro.Apply(fixture, ctx, st)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Imports
	if !strings.Contains(out.Output, "import (") {
		t.Errorf("imports missing")
	}
	if !strings.Contains(out.Output, `"fmt"`) {
		t.Errorf("fmt import missing")
	}

	// Struct fields
	if !strings.Contains(out.Output, "Host    string") {
		t.Errorf("struct field Host missing")
	}
	if !strings.Contains(out.Output, "Port    int") {
		t.Errorf("struct field Port missing")
	}

	// Const values
	if !strings.Contains(out.Output, "DefaultHost = \"localhost\"") {
		t.Errorf("const DefaultHost missing")
	}
	if !strings.Contains(out.Output, "DefaultPort = 8080") {
		t.Errorf("const DefaultPort missing")
	}

	// Interface methods
	if !strings.Contains(out.Output, "Handle(c *Config) error") {
		t.Errorf("interface method Handle missing")
	}
	if !strings.Contains(out.Output, "Name() string") {
		t.Errorf("interface method Name missing")
	}
}
