package compress

import (
	"fmt"
	"strings"
	"testing"
)

// genLog builds an 80-line mixed build/log output: noisy debug/info heartbeats
// interleaved with warnings, errors, and a Python stack-trace block, so the log
// engine's classification + selection + CCR path are all exercised.
func genLog() string {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&b, "DEBUG worker heartbeat tick %d\n", i)
		case 1:
			fmt.Fprintf(&b, "INFO processing request id=%d path=/api/v1/items\n", i)
		case 2:
			fmt.Fprintf(&b, "WARNING retrying connection attempt %d to db host\n", i)
		default:
			fmt.Fprintf(&b, "INFO completed job %d in 0x%x ms\n", i, i*7)
		}
	}
	b.WriteString("ERROR database connection refused at 127.0.0.1:5432\n")
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString(`  File "app.py", line 42, in handler` + "\n")
	b.WriteString("    result = do_work(payload)\n")
	b.WriteString(`  File "work.py", line 17, in do_work` + "\n")
	b.WriteString("    raise RuntimeError('boom')\n")
	b.WriteString("RuntimeError: boom\n")
	for i := 0; i < 13; i++ {
		fmt.Fprintf(&b, "DEBUG cleanup pass %d freed buffers\n", i)
	}
	return b.String()
}

// genDiff builds a multi-hunk unified diff of >50 lines across two files, so the
// diff engine parses past the size short-circuit and exercises hunk selection,
// context reduction, and the CCR path.
func genDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/src/alpha.go b/src/alpha.go\n")
	b.WriteString("--- a/src/alpha.go\n")
	b.WriteString("+++ b/src/alpha.go\n")
	for h := 0; h < 3; h++ {
		fmt.Fprintf(&b, "@@ -%d,6 +%d,7 @@ func alpha%d()\n", h*10+1, h*10+1, h)
		b.WriteString(" context line before\n")
		b.WriteString(" still context\n")
		b.WriteString("-old implementation here\n")
		fmt.Fprintf(&b, "+new error handling branch %d\n", h)
		fmt.Fprintf(&b, "+fix critical auth check %d\n", h)
		b.WriteString(" context line after\n")
		b.WriteString(" trailing context\n")
	}
	b.WriteString("diff --git a/src/beta.go b/src/beta.go\n")
	b.WriteString("--- a/src/beta.go\n")
	b.WriteString("+++ b/src/beta.go\n")
	for h := 0; h < 4; h++ {
		fmt.Fprintf(&b, "@@ -%d,5 +%d,6 @@ func beta%d()\n", h*8+1, h*8+1, h)
		b.WriteString(" some context\n")
		b.WriteString("-removed todo note\n")
		fmt.Fprintf(&b, "+added important fix %d\n", h)
		b.WriteString(" more context\n")
		b.WriteString(" final context\n")
	}
	return b.String()
}

// genSearch builds clustered grep-style matches ("path:line:content") across a
// few files with repeated and error-bearing lines, so the search engine's
// per-file selection, dedup, and scoring are exercised.
func genSearch() string {
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "src/server.go:%d:error: connection failed at attempt %d\n", i, i)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "src/handler.go:%d:    log.Info(\"handling request\")\n", i*3)
	}
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "pkg/util/strings.go:%d:func Helper%d() string { return \"x\" }\n", i+100, i)
	}
	return b.String()
}

// TestEnginesDeterministic is the I4 guard: each engine, run twice on the same
// input with fresh stores, must produce byte-equal compressed output.
func TestEnginesDeterministic(t *testing.T) {
	logIn := genLog()       // 80 mixed lines
	diffIn := genDiff()     // >50-line multi-hunk diff
	searchIn := genSearch() // clustered matches

	if a, b := NewLogCompressor().Compress(logIn, newStore(t)).Compressed, NewLogCompressor().Compress(logIn, newStore(t)).Compressed; a != b {
		t.Fatal("log compressor not deterministic")
	}
	if a, b := NewDiffCompressor().Compress(diffIn, "", newStore(t)).Compressed, NewDiffCompressor().Compress(diffIn, "", newStore(t)).Compressed; a != b {
		t.Fatal("diff compressor not deterministic")
	}
	if a, b := NewSearchCompressor().Compress(searchIn, "", 0, newStore(t)).Compressed, NewSearchCompressor().Compress(searchIn, "", 0, newStore(t)).Compressed; a != b {
		t.Fatal("search compressor not deterministic")
	}
}
