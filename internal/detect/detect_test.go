package detect

import (
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestDetectContentType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want transform.ContentType
	}{
		{"json array", `[{"a":1},{"a":2}]`, transform.JsonArray},
		{"git diff", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", transform.GitDiff},
		{"unified diff no header", "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n line\n", transform.GitDiff},
		{"html", "<!DOCTYPE html>\n<html><body>hi</body></html>", transform.Html},
		{"search results", "src/main.go:42: func main() {\nsrc/util.go:7: var x = 1\n", transform.SearchResults},
		{"build output", "main.go:10:2: undefined: foo\nFAILED build with 1 error\n", transform.BuildOutput},
		{"source code", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n", transform.SourceCode},
		{"plain text", "the quick brown fox jumps over the lazy dog and keeps going", transform.PlainText},
		{"empty is text", "", transform.PlainText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectContentType(c.in)
			if got.Type != c.want {
				t.Errorf("DetectContentType(%q).Type = %v, want %v", c.name, got.Type, c.want)
			}
		})
	}
}
