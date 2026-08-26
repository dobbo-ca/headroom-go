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

// Read must keep entries written before v0.2, which have no mode field.
// An entry with Mode="" must round-trip, and omitempty must suppress the
// key when marshalling. This is the backward-compatibility test.
func TestReadKeepsAnEntryWrittenBeforeTheModeField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	// A literal v0.1.1 line with no "mode" key.
	v011Line := `{"ts":"2026-08-25T10:00:00Z","session":"abc","model":"claude-sonnet-5","messages":4,"bytes_in":100,"bytes_out":90,"tokens_before":200,"tokens_after":100,"reason":"ok"}` + "\n"
	v02Line := `{"ts":"2026-08-25T10:01:00Z","session":"def","model":"claude-sonnet-5","messages":4,"bytes_in":100,"bytes_out":90,"tokens_before":200,"tokens_after":100,"reason":"ok","mode":"subscription"}` + "\n"
	if err := os.WriteFile(path, []byte(v011Line+v02Line), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d entries, want 2 (one v0.1.1, one v0.2)", len(got))
	}
	if got[0].Mode != "" {
		t.Errorf("v0.1.1 entry: Mode=%q, want empty string", got[0].Mode)
	}
	if got[1].Mode != "subscription" {
		t.Errorf("v0.2 entry: Mode=%q, want subscription", got[1].Mode)
	}

	// Round-trip: an Entry{Mode: ""} must marshal with no "mode" key.
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Entry{Session: "ghi", BytesIn: 100, Mode: ""})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	lastLine := lines[len(lines)-2] // -1 is empty, -2 is the appended line
	if strings.Contains(lastLine, `"mode"`) {
		t.Errorf("Entry{Mode:\"\"} marshalled with a mode key: %s", lastLine)
	}
}
