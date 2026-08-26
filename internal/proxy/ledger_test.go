package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// ledgerServer is testServer plus a ledger, and the CCR store a real
// compression needs.
func ledgerServer(t *testing.T, upstream, ledgerPath string) *Server {
	t.Helper()
	w, err := ledger.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	return New(Deps{
		Config:    Config{Upstream: upstream, MaxBodyBytes: 1 << 24, Compress: true, RequestTimeout: 5 * time.Second},
		Store:     newMapStore(),
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "test",
		Ledger:    w,
	})
}

// compressibleBody is a tool_result the log compressors actually fire on:
// a failure line plus repetitive INFO lines, which is the shape real build
// output has.
func compressibleBody(sessionMessages int) []byte {
	var b strings.Builder
	b.WriteString("FAILED: build broke\\n")
	for i := 0; i < 400; i++ {
		b.WriteString("INFO 2026-08-25 worker processed record status=ok\\n")
	}
	var msgs strings.Builder
	msgs.WriteString(`{"role":"user","content":[{"type":"text","text":"go"}]}`)
	for i := 1; i < sessionMessages; i++ {
		msgs.WriteString(`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` +
			b.String() + `"}]}`)
	}
	return []byte(`{"model":"claude-sonnet-5","messages":[` + msgs.String() + `]}`)
}

// The ledger is the only record `headroom perf` has. If the proxy does not
// write one, a whole day of use reports as nothing at all.
func TestProxyRecordsEveryTurnInTheLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	srv := ledgerServer(t, up.URL, path)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	body := compressibleBody(2)
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "fe814859-5860-4fa3-a2dc-7906e146c71a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	entries, err := ledger.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the proxy wrote %d ledger entries for one turn, want 1", len(entries))
	}
	e := entries[0]

	// The join `headroom perf` performs depends on this exact digest.
	if want := cachestab.ClaudeSessionDigest("fe814859-5860-4fa3-a2dc-7906e146c71a"); e.Session != want {
		t.Errorf("session = %q, want %q; the report would never join this turn", e.Session, want)
	}
	if e.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", e.Model)
	}
	if e.Messages != 2 {
		t.Errorf("messages = %d, want 2", e.Messages)
	}
	if e.BytesIn != len(body) {
		t.Errorf("bytes_in = %d, want %d", e.BytesIn, len(body))
	}
	// The fixture must actually compress, or this test asserts nothing about
	// the fields that matter.
	if e.Reason != "ok" {
		t.Fatalf("reason = %q, want ok: the fixture stopped exercising the compression path", e.Reason)
	}
	if e.BytesOut >= e.BytesIn {
		t.Errorf("bytes_out = %d, bytes_in = %d: nothing was removed", e.BytesOut, e.BytesIn)
	}
	if e.TokensBefore <= e.TokensAfter {
		t.Errorf("tokens_before = %d, tokens_after = %d", e.TokensBefore, e.TokensAfter)
	}
	if len(e.Strategies) == 0 {
		t.Error("no strategy recorded for a turn that compressed")
	}
}

// The drift the report shows must be the CLIENT's, so it has to come from the
// inbound body. A second turn that rewrites the prefix must appear.
func TestLedgerRecordsClientDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	srv := ledgerServer(t, up.URL, path)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	post := func(body string) {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("X-Claude-Code-Session-Id", "aaaaaaaa-0000-0000-0000-000000000000")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	const first = `{"model":"m","system":"you are helpful","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	// The system prompt changes: a real prefix rewrite, on an axis the
	// detector names.
	const second = `{"model":"m","system":"you are VERY helpful","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"text","text":"more"}]}]}`
	post(first)
	post(second)

	entries, err := ledger.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if len(entries[0].Drift) != 0 {
		t.Errorf("the first turn of a session cannot have drifted: %v", entries[0].Drift)
	}
	if len(entries[1].Drift) == 0 {
		t.Fatal("a rewritten system prompt was not recorded as drift")
	}
	if !strings.Contains(strings.Join(entries[1].Drift, ","), "system") {
		t.Errorf("drift = %v, want the system axis named", entries[1].Drift)
	}
}

// A nil ledger must not cost a request. This is the disk-full path.
func TestNilLedgerDoesNotBreakForwarding(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL) // no Ledger in Deps
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d with no ledger wired", resp.StatusCode)
	}
}
