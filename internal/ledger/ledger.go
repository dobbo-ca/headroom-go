// Package ledger records one line per compressed request so a later `headroom
// perf` run can answer "did this help or hurt?" without anyone reading a log
// by hand.
//
// The ledger is OFF the compression path. Nothing in it is read back during a
// request, and the timestamp it carries never reaches the bytes forwarded
// upstream, so determinism (I4) is untouched.
package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/paths"
)

// Entry is one request's record. Field names are short because this file grows
// by one line per turn for as long as the machine runs.
type Entry struct {
	TS string `json:"ts"`
	// Session is the SHORT DIGEST of the session key, never the key itself.
	// `headroom perf` recomputes the same digest from a Claude Code
	// transcript filename to join the two sources.
	Session      string   `json:"session"`
	Model        string   `json:"model"`
	Messages     int      `json:"messages"`
	BytesIn      int      `json:"bytes_in"`
	BytesOut     int      `json:"bytes_out"`
	TokensBefore int      `json:"tokens_before"`
	TokensAfter  int      `json:"tokens_after"`
	Reason       string   `json:"reason"`
	Strategies   []string `json:"strategies,omitempty"`
	Replayed     int      `json:"replayed,omitempty"`
	// Drift lists the axes on which the CLIENT's cached prefix changed
	// between turns. Observed on the inbound body, so it never reports
	// headroom's own rewrites.
	Drift []string `json:"drift,omitempty"`
}

// Writer appends entries to a JSONL file. Safe for concurrent use: every
// Append is one O_APPEND write of one whole line, which the OS orders for us.
//
// ponytail: this relies on a single write to an O_APPEND file being atomic.
// If a short write ever splits a line, Read drops that one line and the report
// loses a turn. Add a mutex, or a single long-lived handle, if that shows up.
type Writer struct {
	path string
	now  func() time.Time
}

// DefaultPath is the ledger file inside the headroom home directory.
func DefaultPath() (string, error) {
	h, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "ledger.jsonl"), nil
}

// Open returns a Writer appending to path, creating the directory if needed.
func Open(path string) (*Writer, error) {
	if err := paths.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return &Writer{path: path, now: time.Now}, nil
}

// Path reports the file this Writer appends to.
func (w *Writer) Path() string { return w.path }

// Append writes one entry. A nil Writer is a no-op, so a caller that could not
// open a ledger does not need a branch at every call site.
//
// Errors are swallowed deliberately: a full or unwritable disk must degrade
// observability, never a live session.
func (w *Writer) Append(e Entry) {
	if w == nil {
		return
	}
	e.TS = w.now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(e)
	if err != nil {
		return
	}

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Read parses every entry in a ledger file. A missing file reads as empty: a
// machine that has never run the proxy has no ledger, and that is not an error
// worth stopping a report for. Unparseable lines are skipped, so a torn write
// at the tail cannot lose the whole history.
func Read(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, line := range splitLines(b) {
		var e Entry
		if json.Unmarshal(line, &e) == nil && e.Session != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
