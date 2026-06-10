package reformats

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestLogTemplateCollapsesRun(t *testing.T) {
	var lt LogTemplate
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString("INFO worker processing job ")
		b.WriteString(strings.Repeat("x", 1+i)) // varying token (distinct per line)
		b.WriteByte('\n')
	}
	out, err := lt.Apply(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "[Template T1:") {
		t.Fatalf("expected a template header, got:\n%s", out.Output)
	}
	if out.BytesSaved <= 0 {
		t.Fatalf("expected savings, got %d", out.BytesSaved)
	}
}

func TestLogTemplateBelowMinLinesSkipped(t *testing.T) {
	var lt LogTemplate
	if _, err := lt.Apply("a\nb\nc\n"); !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("want skip, got %v", err)
	}
}

func TestLogTemplateEmptySkipped(t *testing.T) {
	var lt LogTemplate
	if _, err := lt.Apply(""); !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("want skip on empty input, got %v", err)
	}
}

// reconstructFromOutput parses LogTemplate output back into the original lines.
// For every "[Template T<id>: <slots>] (<N> occurrences)" header it reads the
// next N variant rows and splices each row's tokens back into the wildcard ("<*>")
// positions, reproducing the original line. Non-header lines pass through verbatim.
func reconstructFromOutput(t *testing.T, output string) []string {
	t.Helper()
	// Mirror Rust str::lines on the output for line splitting.
	lines := splitLinesRust(output)
	var rebuilt []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "[Template T") && strings.Contains(line, "] (") && strings.HasSuffix(line, " occurrences)") {
			// Parse header: [Template T<id>: <slots>] (<N> occurrences)
			openIdx := strings.Index(line, ": ")
			closeIdx := strings.LastIndex(line, "] (")
			if openIdx < 0 || closeIdx < 0 {
				t.Fatalf("malformed header: %q", line)
			}
			slotStr := line[openIdx+2 : closeIdx]
			slots := strings.Split(slotStr, " ")
			occStr := line[closeIdx+3:]
			occStr = strings.TrimSuffix(occStr, " occurrences)")
			n := 0
			for _, c := range occStr {
				n = n*10 + int(c-'0')
			}
			i++
			for r := 0; r < n; r++ {
				variantToks := strings.Fields(lines[i])
				i++
				vIdx := 0
				out := make([]string, len(slots))
				for p, s := range slots {
					if s == "<*>" {
						out[p] = variantToks[vIdx]
						vIdx++
					} else {
						out[p] = s
					}
				}
				rebuilt = append(rebuilt, strings.Join(out, " "))
			}
			continue
		}
		rebuilt = append(rebuilt, line)
		i++
	}
	return rebuilt
}

func TestLogTemplateLosslessReconstruct(t *testing.T) {
	var lt LogTemplate
	var b strings.Builder
	var original []string
	// Distinct-varying lines that collapse: constant "INFO worker processing job",
	// varying trailing id token (distinct each line so the position becomes a wildcard).
	for i := 0; i < 30; i++ {
		line := "INFO worker processing job " + strings.Repeat("z", i+1)
		original = append(original, line)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	in := b.String()
	out, err := lt.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "[Template T1:") {
		t.Fatalf("expected collapse, got:\n%s", out.Output)
	}

	rebuilt := reconstructFromOutput(t, out.Output)
	if len(rebuilt) != len(original) {
		t.Fatalf("line count mismatch: got %d want %d\noutput:\n%s", len(rebuilt), len(original), out.Output)
	}
	for i := range original {
		if rebuilt[i] != original[i] {
			t.Fatalf("line %d mismatch:\n got  %q\n want %q", i, rebuilt[i], original[i])
		}
	}
}

func TestLogTemplateNoConstantsEmitsVerbatim(t *testing.T) {
	var lt LogTemplate
	var b strings.Builder
	// Every position varies across lines (e.g. "0 1 2", "1 2 3", ...) -> constant_count=0 < 2.
	for i := 0; i < 25; i++ {
		b.WriteString(itoa(i))
		b.WriteByte(' ')
		b.WriteString(itoa(i + 1))
		b.WriteByte(' ')
		b.WriteString(itoa(i + 2))
		b.WriteByte('\n')
	}
	in := b.String()
	out, err := lt.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Output, "[Template T") {
		t.Fatalf("all-varying run must emit verbatim, got:\n%s", out.Output)
	}
}

func TestLogTemplateBlankLinesBreakRuns(t *testing.T) {
	var lt LogTemplate
	var b strings.Builder
	// First block of identical-shape lines.
	for i := 0; i < 12; i++ {
		b.WriteString("INFO worker processing job ")
		b.WriteString(strings.Repeat("a", i+1))
		b.WriteByte('\n')
	}
	b.WriteByte('\n') // blank line breaks the run
	for i := 0; i < 12; i++ {
		b.WriteString("INFO worker processing job ")
		b.WriteString(strings.Repeat("b", i+1))
		b.WriteByte('\n')
	}
	in := b.String()
	out, err := lt.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	// Each block of 12 collapses into its own template (T1 then T2). Blank line emitted verbatim.
	if !strings.Contains(out.Output, "[Template T1:") || !strings.Contains(out.Output, "[Template T2:") {
		t.Fatalf("expected two templates separated by blank line, got:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "\n\n") {
		t.Fatalf("blank line should survive verbatim, got:\n%s", out.Output)
	}
}

func TestLogTemplateNeverInflates(t *testing.T) {
	var lt LogTemplate
	// 25 unique single-token lines: no collapse possible (token-count mismatch / constants),
	// output equals input; never-inflate must hold.
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString("uniqueline")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	in := b.String()
	out, err := lt.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) > len(in) {
		t.Fatalf("inflated: in=%d out=%d", len(in), len(out.Output))
	}
	if out.BytesSaved < 0 {
		t.Fatalf("bytes_saved must be >= 0, got %d", out.BytesSaved)
	}
}

// itoa is a tiny base-10 helper for tests (avoids importing strconv just for tests).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

var _ transform.ReformatTransform = LogTemplate{}
