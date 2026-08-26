package detect_test

import (
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/detect"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// Lossy-compressing raw file content makes the agent re-read the file to
// recover detail, and sometimes resolve the task wrongly. Derived output —
// grep, ls, tests — is safe. This table is the boundary.
func TestIsReadCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"cat", "cat internal/foo.go", true},
		{"head", "head -50 README.md", true},
		{"tail", "tail -n 100 server.rb", true},
		{"nl", "nl main.c", true},
		{"sed range print", "sed -n '1,80p' schema.sql", true},
		{"bare sed is a stream edit", "sed 's/a/b/' file.txt", false},

		// Wrappers are shell grammar, not policy. rtk matters here: this
		// machine rewrites dev commands through it.
		{"rtk wrapper", "rtk cat internal/foo.go", true},
		{"sudo wrapper", "sudo cat /etc/hosts", true},
		{"timeout wrapper", "timeout 30 cat big.log", true},
		{"env assignment", "FOO=1 cat notes.md", true},
		{"absolute path", "/bin/cat notes.md", true},
		{"cd prefix", "cd /tmp/x && cat notes.md", true},
		{"bash -lc", `bash -lc "cat notes.md"`, true},

		// Writes are not reads, however they are spelled.
		{"redirect", "cat > out.txt", false},
		{"append", "cat a.txt >> b.txt", false},
		{"heredoc", "cat > f <<EOF", false},
		{"tee", "cat a.txt | tee b.txt", false},

		// Derived output stays compressible.
		{"grep", "grep -rn foo .", false},
		{"ls", "ls -la", false},
		{"find", "find . -name '*.go'", false},
		{"go test", "go test -v ./...", false},

		// Lockfiles read as plain text but are regenerated, never patched.
		{"lockfile go.sum", "cat go.sum", false},
		{"lockfile package-lock", "cat ./package-lock.json", false},
		{"lockfile Cargo.lock", "head -100 Cargo.lock", false},

		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detect.IsReadCommand(tt.cmd); got != tt.want {
				t.Errorf("IsReadCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// Protection is decided in two steps: is this a file read, and is the CONTENT a
// data type nobody byte-patches. PlainText must stay protected — the code
// detector knows only a few languages, so Ruby, C, SQL and Markdown all land
// there, and releasing PlainText would lossy-compress code the agent is about
// to patch.
func TestReadOutputIsProtected(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		cmd   string
		ctype transform.ContentType
		want  bool
	}{
		{"Read of prose", "Read", "", transform.PlainText, true},
		{"Read of unrecognised code lands in PlainText", "Read", "", transform.PlainText, true},
		{"Read of a build log is releasable", "Read", "", transform.BuildOutput, false},
		{"Read of a diff is releasable", "Read", "", transform.GitDiff, false},
		{"Read of search output is releasable", "Read", "", transform.SearchResults, false},
		{"bash cat of prose", "Bash", "cat notes.md", transform.PlainText, true},
		{"bash grep is derived", "Bash", "grep -rn foo .", transform.PlainText, false},
		{"bash go test is derived", "Bash", "go test -v ./...", transform.PlainText, false},
		{"unknown tool is not a read", "", "", transform.PlainText, false},
		{"WebFetch is not a file read", "WebFetch", "", transform.PlainText, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detect.ReadOutputIsProtected(tt.tool, tt.cmd, tt.ctype); got != tt.want {
				t.Errorf("ReadOutputIsProtected(%q,%q,%v) = %v, want %v",
					tt.tool, tt.cmd, tt.ctype, got, tt.want)
			}
		})
	}
}
