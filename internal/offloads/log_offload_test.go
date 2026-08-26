package offloads

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestLogOffloadEmptyBloatZero(t *testing.T) {
	lo := NewLogOffload(compress.NewLogCompressor())
	if got := lo.EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestLogOffloadConfidence(t *testing.T) {
	if c := NewLogOffload(compress.NewLogCompressor()).Confidence(); c != 0.85 {
		t.Fatalf("Confidence = %v, want 0.85", c)
	}
}

func TestLogOffloadShortInputSkips(t *testing.T) {
	// Below the wrapped compressor's min_lines_for_ccr: no key -> ErrSkipped.
	st := store(t)
	lo := NewLogOffload(compress.NewLogCompressor())
	in := strings.Repeat("2024-01-01 INFO something happened\n", 5)
	_, err := lo.Apply(in, transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("short input -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

func TestLogOffloadAppliesTo(t *testing.T) {
	at := NewLogOffload(compress.NewLogCompressor()).AppliesTo()
	if len(at) != 1 || at[0] != transform.BuildOutput {
		t.Fatalf("AppliesTo = %v, want [BuildOutput]", at)
	}
}

// protectedReadFixture is source code as the Read tool returns it: every line
// carries an "N\t" prefix, which is exactly what stops the detector seeing
// SourceCode. It arrives here classified BuildOutput.
func protectedReadFixture() string {
	var b strings.Builder
	b.WriteString("1\tpackage store\n")
	for i := 2; i <= 300; i++ {
		fmt.Fprintf(&b, "%d\t\tif err := rows.Scan(&id, &name); err != nil {\n", i)
	}
	return b.String()
}

// The two tests below are a PAIR, and they must stay one: the first proves the
// read-protection gate fires inside log_offload, the second proves it is not
// blanket. Either one alone passes with the gate deleted or with the gate
// always returning true, so neither is coverage on its own.

func TestLogOffloadDeclinesAProtectedCodeFileRead(t *testing.T) {
	// Measured 2026-08-26: 10.31 MB of .go read via Read classified BuildOutput
	// and was shredded ~80% by this compressor, including reads that carried an
	// explicit offset/limit. That is the harm the gate exists to stop.
	st := store(t)
	lo := NewLogOffload(compress.NewLogCompressor())
	ctx := transform.CompressionContext{
		ProducingTool: "Read",
		ToolInput:     `{"file_path":"/tmp/store.go"}`,
	}
	_, err := lo.Apply(protectedReadFixture(), ctx, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("a .go file read must be skipped as protected, got err=%v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("a skipped block must leave no store entry, store.Len()=%d", st.Len())
	}
}

func TestLogOffloadStillCompressesBashBuildOutput(t *testing.T) {
	// The gate must not be blanket. Build output is log_offload's whole reason
	// to exist, and it is 90% of the bytes this strategy compresses today.
	st := store(t)
	lo := NewLogOffload(compress.NewLogCompressor())
	ctx := transform.CompressionContext{
		ProducingTool: "Bash",
		ToolCommand:   "go build ./...",
	}
	out, err := lo.Apply(protectedReadFixture(), ctx, st)
	if err != nil {
		t.Fatalf("build output must still compress, got err=%v", err)
	}
	if len(out.Output) >= len(protectedReadFixture()) {
		t.Fatalf("build output did not shrink: %d >= %d", len(out.Output), len(protectedReadFixture()))
	}
	if st.Len() == 0 {
		t.Fatal("a compressed block must store its original, store.Len()=0")
	}
}

var _ transform.OffloadTransform = (*LogOffload)(nil)
