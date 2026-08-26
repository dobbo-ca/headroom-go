package detect_test

import (
	"os"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/detect"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// The fixture is real `go test -v` output captured from live Claude Code
// traffic on 2026-08-26: a large verbose run in which one package failed.
// That is the exact shape headroom exists to compress, and it was classified
// GitDiff because every Go test prints a "--- PASS:" or "--- FAIL:" line.
//
// The consequence was not cosmetic. The router only reaches the log
// compressor for BuildOutput, so the block went to the diff compressor, which
// correctly declined it, and headroom saved nothing at all.
func TestVerboseGoTestOutputIsNotADiff(t *testing.T) {
	b, err := os.ReadFile("testdata/go_test_verbose_failing.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)

	// The fixture must actually carry the trigger, or this test could pass on
	// a file that never posed the problem.
	if n := strings.Count(text, "\n--- PASS:") + strings.Count(text, "\n--- FAIL:"); n < 100 {
		t.Fatalf("fixture carries only %d `--- ` lines; it no longer reproduces the false positive", n)
	}
	if !strings.Contains(text, "--- FAIL:") {
		t.Fatal("fixture has no failure; it is not the shape headroom is built for")
	}

	got := detect.DetectContentType(text)
	if got.Type == transform.GitDiff {
		t.Fatalf("verbose test output classified as %v; the log compressor is unreachable for it", got.Type)
	}
	if got.Type != transform.BuildOutput {
		t.Errorf("classified as %v, want BuildOutput", got.Type)
	}
}

// Real diffs must still be diffs. A unified diff pairs "--- " with "+++ ";
// a git header or a hunk marker stands alone.
func TestRealDiffsStillDetect(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want transform.ContentType
	}{
		{"git header", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", transform.GitDiff},
		{"unified, no git header", "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n line\n", transform.GitDiff},
		{"hunk header alone", "@@ -1,2 +1,2 @@\n context\n", transform.GitDiff},
		{"git header alone", "diff --git a/x b/x\nsimilarity index 100%\n", transform.GitDiff},
		// The false positive in miniature: no longer a diff, and now routed
		// where the log compressor can reach it.
		{"go test lines only", "--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)\n", transform.BuildOutput},
		{"go test run markers", "=== RUN   TestA\n=== RUN   TestB\n", transform.BuildOutput},
		// A shell heading, the case hr-txs was originally filed for. Neither
		// a diff nor build output.
		{"echo section heading", "--- build ---\nall good\n", transform.PlainText},
		// A loose /fail/i would have caught this; the anchored pattern must not.
		{"prose that mentions failing", "This step may fail if the disk is full.\nRetry it.\n", transform.PlainText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detect.DetectContentType(tt.in); got.Type != tt.want {
				t.Errorf("DetectContentType = %v, want %v", got.Type, tt.want)
			}
		})
	}
}
