package compress

import (
	"strings"
	"testing"
)

const twoFileDiff = `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 func main() {
-	old()
+	new()
 }
diff --git a/go.sum b/go.sum
index 333..444 100644
--- a/go.sum
+++ b/go.sum
@@ -1,2 +1,2 @@
-old/dep v1.0.0 h1:abc=
+new/dep v1.1.0 h1:def=`

func TestParseDiffFindsBothHunks(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(hunks))
	}
}

func TestParseDiffAttributesFiles(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	if hunks[0].file != "main.go" {
		t.Errorf("hunk 0 file = %q, want main.go", hunks[0].file)
	}
	if hunks[1].file != "go.sum" {
		t.Errorf("hunk 1 file = %q, want go.sum", hunks[1].file)
	}
}

func TestParseDiffAttachesFileHeaderToItsHunk(t *testing.T) {
	_, hunks := parseDiff(twoFileDiff)
	joined := strings.Join(hunks[1].header, "\n")
	if !strings.Contains(joined, "diff --git a/go.sum b/go.sum") {
		t.Errorf("hunk 1 header = %q, want the go.sum file header", joined)
	}
}

func TestParseDiffPreambleIsEmptyWhenDiffStartsWithAFile(t *testing.T) {
	preamble, _ := parseDiff(twoFileDiff)
	if len(preamble) != 0 {
		t.Errorf("preamble = %v, want empty", preamble)
	}
}

func TestParseDiffKeepsLeadingTextAsPreamble(t *testing.T) {
	in := "commit abc123\nAuthor: Someone\n\n" + twoFileDiff
	preamble, hunks := parseDiff(in)
	if len(preamble) == 0 || preamble[0] != "commit abc123" {
		t.Errorf("preamble = %v, want it to start with the commit line", preamble)
	}
	if len(hunks) != 2 {
		t.Errorf("got %d hunks, want 2", len(hunks))
	}
}

func TestParseDiffOnNonDiffInputFindsNoHunks(t *testing.T) {
	_, hunks := parseDiff("this is not a diff at all")
	if len(hunks) != 0 {
		t.Errorf("got %d hunks, want 0", len(hunks))
	}
}

func TestParseDiffOnEmptyInputFindsNoHunks(t *testing.T) {
	_, hunks := parseDiff("")
	if len(hunks) != 0 {
		t.Errorf("got %d hunks, want 0", len(hunks))
	}
}

func TestIsLockfileMatchesKnownNames(t *testing.T) {
	for _, p := range []string{
		"go.sum", "vendor/go.sum", "package-lock.json", "a/b/yarn.lock",
		"pnpm-lock.yaml", "Cargo.lock", "poetry.lock", "Gemfile.lock",
		"composer.lock",
	} {
		if !isLockfile(p) {
			t.Errorf("isLockfile(%q) = false, want true", p)
		}
	}
}

func TestIsLockfileRejectsOrdinaryFiles(t *testing.T) {
	for _, p := range []string{"main.go", "go.mod", "lock.go", "src/lockfile.ts"} {
		if isLockfile(p) {
			t.Errorf("isLockfile(%q) = true, want false", p)
		}
	}
}

func TestIsWhitespaceOnlyDetectsReindent(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"-  x := 1",
		"+\tx := 1",
	}}
	if !isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = false, want true for a pure reindent")
	}
}

func TestIsWhitespaceOnlyRejectsRealChange(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"-	old()",
		"+	new()",
	}}
	if isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = true, want false for a real change")
	}
}

func TestIsWhitespaceOnlyIgnoresFileMarkerLines(t *testing.T) {
	// --- and +++ start with - and + but are not content lines.
	h := hunk{file: "main.go", body: []string{
		"@@ -1,2 +1,2 @@",
		"--- a/main.go",
		"+++ b/main.go",
		"-  x := 1",
		"+\tx := 1",
	}}
	if !isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = false; --- and +++ must not count as content")
	}
}

func TestIsWhitespaceOnlyRejectsUnevenAddsAndRemoves(t *testing.T) {
	h := hunk{file: "main.go", body: []string{
		"@@ -1,3 +1,2 @@",
		"-  x := 1",
		"-  y := 2",
		"+\tx := 1",
	}}
	if isWhitespaceOnly(h) {
		t.Error("isWhitespaceOnly = true, want false when a line was deleted outright")
	}
}

func TestIsNoiseCatchesLockfileAndWhitespace(t *testing.T) {
	lock := hunk{file: "go.sum", body: []string{"@@ -1 +1 @@", "-a", "+b"}}
	if !isNoise(lock) {
		t.Error("isNoise = false for a lockfile hunk")
	}
	ws := hunk{file: "main.go", body: []string{"@@ -1 +1 @@", "-  x", "+\tx"}}
	if !isNoise(ws) {
		t.Error("isNoise = false for a whitespace-only hunk")
	}
	real := hunk{file: "main.go", body: []string{"@@ -1 +1 @@", "-old()", "+new()"}}
	if isNoise(real) {
		t.Error("isNoise = true for a real code change")
	}
}
