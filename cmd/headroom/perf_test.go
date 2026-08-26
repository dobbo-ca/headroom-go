package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
)

// perf must read the ledger it is pointed at, join it to the transcripts it is
// pointed at, and put the answer on stdout.
func TestPerfReportsFromTheFlags(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "fe814859-5860-4fa3-a2dc-7906e146c71a"
	digest := cachestab.ClaudeSessionDigest(sessionID)

	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	line := `{"ts":"2026-08-25T10:00:00Z","session":"` + digest +
		`","model":"claude-sonnet-5","messages":4,"bytes_in":2000,"bytes_out":1000,` +
		`"tokens_before":400,"tokens_after":100,"reason":"ok","strategies":["log_offload"],"replayed":3}` + "\n"
	if err := os.WriteFile(ledgerPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	tdir := filepath.Join(dir, "projects", "p")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := `{"type":"assistant","message":{"usage":{"input_tokens":100,` +
		`"cache_read_input_tokens":9000,"cache_creation_input_tokens":900,` +
		`"cache_creation":{"ephemeral_1h_input_tokens":900,"ephemeral_5m_input_tokens":0}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(tdir, sessionID+".jsonl"), []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"perf", "--ledger", ledgerPath, "--transcripts", filepath.Join(dir, "projects")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("perf: %v", err)
	}

	got := out.String()
	for _, want := range []string{"50.0%", "log_offload", "cache read", "VERDICT"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	// The session joined, so the cache half must be a real number.
	if strings.Contains(got, "No transcript matched") {
		t.Errorf("the join failed:\n%s", got)
	}
}

// --json must emit the same report as data, so it can be graphed or diffed.
func TestPerfJSON(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	if err := os.WriteFile(ledgerPath,
		[]byte(`{"ts":"2026-08-25T10:00:00Z","session":"abc","bytes_in":100,"bytes_out":25}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"perf", "--ledger", ledgerPath, "--transcripts", dir, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("perf --json: %v", err)
	}
	var r struct {
		Turns   int   `json:"Turns"`
		BytesIn int64 `json:"BytesIn"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", err, out.String())
	}
	if r.Turns != 1 || r.BytesIn != 100 {
		t.Errorf("decoded %+v", r)
	}
}

// A machine that has never run the proxy must get a sentence, not a crash and
// not a page of zeroes.
func TestPerfWithNoLedger(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"perf", "--ledger", filepath.Join(dir, "absent.jsonl"), "--transcripts", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("perf on a missing ledger: %v", err)
	}
	if !strings.Contains(out.String(), "No turns recorded") {
		t.Errorf("got:\n%s", out.String())
	}
}

// End-to-end: a ledger with mode=subscription must produce the
// "context-window headroom" headline, not the metered label. This test
// catches a missing or misspelled JSON tag on Entry.Mode.
func TestPerfHeadlinesWindowHeadroomForASubscriptionLedger(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "aaaa0000-0000-0000-0000-000000000000"
	digest := cachestab.ClaudeSessionDigest(sessionID)

	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	line := `{"ts":"2026-08-25T10:00:00Z","session":"` + digest +
		`","model":"claude-sonnet-5","messages":4,"bytes_in":2000,"bytes_out":1000,` +
		`"tokens_before":100,"tokens_after":50,"reason":"ok","mode":"subscription"}` + "\n"
	if err := os.WriteFile(ledgerPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	tdir := filepath.Join(dir, "projects", "p")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := `{"type":"assistant","message":{"usage":{"input_tokens":100,` +
		`"cache_read_input_tokens":900,"cache_creation_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(filepath.Join(tdir, sessionID+".jsonl"), []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"perf", "--ledger", ledgerPath, "--transcripts", filepath.Join(dir, "projects")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("perf: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "context-window headroom") {
		t.Errorf("subscription ledger did not produce the subscription headline:\n%s", got)
	}
	if strings.Contains(got, "what a user actually sees") {
		t.Errorf("old label leaked into output:\n%s", got)
	}
}
