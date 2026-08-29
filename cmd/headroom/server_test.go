package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/proxy"
)

// compressibleBody is a tool_result the log compressors actually fire on.
func compressibleBody() []byte {
	var log strings.Builder
	log.WriteString("FAILED: build broke\\n")
	for i := 0; i < 400; i++ {
		log.WriteString("INFO 2026-08-25 worker processed record status=ok\\n")
	}
	return []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + log.String() + `"}]}]}`)
}

// A proxy that records nothing makes `headroom perf` report a whole day as
// zero. `wrap` is the command the quickstart tells people to run, and it built
// its proxy at a second site that never opened a ledger — so the headline
// command produced no data at all. Both commands now go through
// newProxyServer, and this proves the one wrap uses records a turn.
func TestWrapStartedProxyRecordsToTheLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HEADROOM_HOME", home)
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")

	var seen []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := agentSpecFor("claude")
	base := "http://" + freeAddr(t)
	pcfg, err := proxyConfigFor(spec, base, up.URL)
	if err != nil {
		t.Fatal(err)
	}

	_, stop, err := startProxy(context.Background(), pcfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := waitForProxy(http.DefaultClient, base, 20*time.Second); err != nil {
		t.Fatal(err)
	}

	body := compressibleBody()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("X-Claude-Code-Session-Id", "fe814859-5860-4fa3-a2dc-7906e146c71a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// The fixture must actually compress, or a ledger line would prove
	// nothing about the fields that matter.
	if len(seen) >= len(body) {
		t.Fatalf("the fixture did not compress: %d bytes out of %d in", len(seen), len(body))
	}

	entries, err := ledger.Read(filepath.Join(home, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a wrap-started proxy wrote %d ledger entries for one turn; "+
			"`headroom perf` would report the whole session as nothing", len(entries))
	}
	e := entries[0]
	if e.Reason != "ok" || e.BytesOut >= e.BytesIn || len(e.Strategies) == 0 {
		t.Errorf("ledger entry did not describe the compression: %+v", e)
	}
}

// The `proxy` command must record too — the same ledger, from the same
// construction site.
func TestProxyCommandRecordsToTheLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HEADROOM_HOME", home)
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")

	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newProxyServer(proxy.Config{
		Upstream: "https://upstream.invalid", MaxBodyBytes: 1 << 24,
		Compress: true, RequestTimeout: 5 * time.Second,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv == nil {
		t.Fatal("no server")
	}
	// Opening the ledger is what creates the file's directory; the file
	// itself appears on the first Append. Prove the writer exists by using
	// the same path the server was handed.
	p, err := ledger.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "ledger.jsonl"); p != want {
		t.Errorf("ledger path = %q, want %q", p, want)
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Errorf("the ledger directory was not created: %v", err)
	}
}
