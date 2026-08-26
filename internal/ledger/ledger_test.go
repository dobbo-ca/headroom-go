package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Entry{Session: "aaa", BytesIn: 100, BytesOut: 40, Strategies: []string{"log_offload"}})
	w.Append(Entry{Session: "bbb", BytesIn: 200, BytesOut: 200, Reason: "no_candidates"})

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d entries, want 2", len(got))
	}
	if got[0].Session != "aaa" || got[0].BytesOut != 40 || got[0].Strategies[0] != "log_offload" {
		t.Errorf("first entry round-tripped as %+v", got[0])
	}
	if got[1].Reason != "no_candidates" {
		t.Errorf("second entry round-tripped as %+v", got[1])
	}
	// Every entry must carry a timestamp, or `--since` silently keeps
	// everything forever.
	for i, e := range got {
		if e.TS == "" {
			t.Errorf("entry %d has no timestamp", i)
		}
	}
}

// A ledger that could not be opened must not need a branch at the call site,
// and must not panic in the request path.
func TestNilWriterIsANoOp(t *testing.T) {
	var w *Writer
	w.Append(Entry{Session: "aaa"})
}

// A torn write at the tail must cost one line, not the whole history.
func TestReadSkipsUnparseableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	content := `{"session":"aaa","bytes_in":1}` + "\n" +
		`{"session":"bbb","bytes_in":2` + "\n" + // truncated
		`{"session":"ccc","bytes_in":3}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Session != "aaa" || got[1].Session != "ccc" {
		t.Errorf("read %+v, want the two intact lines", got)
	}
}

// A machine that has never run the proxy has no ledger. That is an empty
// report, not an error.
func TestReadMissingFileIsEmpty(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("a missing ledger must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries from a missing file", len(got))
	}
}

// The proxy appends from many request goroutines at once. A torn or
// interleaved line loses a turn from the report.
func TestConcurrentAppendsAreWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.Append(Entry{Session: "s", BytesIn: i, Reason: strings.Repeat("x", 200)})
		}(i)
	}
	wg.Wait()

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("read %d entries, want %d — a line was torn", len(got), n)
	}
}
