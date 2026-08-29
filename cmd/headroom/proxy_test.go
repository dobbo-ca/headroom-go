package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Each flag must reach configuration and change an observable outcome.
func TestProxyUpstreamFlagReachesConfig(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://from-env.example")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--upstream", "ftp://bad-scheme.example", "--dry-run"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected the flag's bad scheme to fail validation")
	}
	if !strings.Contains(err.Error(), "ftp://bad-scheme.example") {
		t.Errorf("error names %q; the flag value must beat the environment", err)
	}
}

func TestProxyListenFlagReachesConfig(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--upstream", "https://ok.example", "--listen", "127.0.0.1:9999", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	// --dry-run prints the resolved config; assert the flag landed.
	out := dryRunConfig
	if out.Listen != "127.0.0.1:9999" {
		t.Errorf("Listen = %q, want the flag value", out.Listen)
	}
	if out.Upstream != "https://ok.example" {
		t.Errorf("Upstream = %q", out.Upstream)
	}
}

func TestProxyMissingUpstreamIsAnError(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--dry-run"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error with no upstream configured")
	}
	if !strings.Contains(err.Error(), "HEADROOM_PROXY_UPSTREAM") {
		t.Errorf("error must name the missing variable, got %q", err)
	}
}

func TestProxyCCRBackendFlagReachesConfig(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--upstream", "https://ok.example", "--ccr-backend", "nonsense", "--dry-run"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an invalid CCR backend to fail validation")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("error must name the bad backend, got %q", err)
	}
}

// The memory backend must not need a SQLite path.
func TestProxyMemoryBackendNeedsNoPath(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--upstream", "https://ok.example", "--ccr-backend", "memory", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("memory backend dry run failed: %v", err)
	}
}

// --dry-run must resolve configuration and stop there. Opening the CCR store
// would create ~/.headroom/ccr.db as a side effect of a validation-only run.
func TestProxyDryRunTouchesNoDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HEADROOM_HOME", home)
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"proxy", "--upstream", "https://ok.example", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "ccr.db")); !os.IsNotExist(err) {
		t.Errorf("--dry-run opened the CCR store: Stat(ccr.db) = %v", err)
	}
}

// Without --dry-run the command must actually serve, and --ccr-path must put
// the store where the flag says.
func TestProxyCCRPathFlagReachesTheStore(t *testing.T) {
	// HEADROOM_HOME points somewhere else, so only the flag can put the store
	// at the path we assert on.
	t.Setenv("HEADROOM_HOME", t.TempDir())
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")
	want := filepath.Join(t.TempDir(), "nested", "custom.db")

	runProxy(t, "--upstream", "https://unused.example", "--ccr-path", want)

	if _, err := os.Stat(want); err != nil {
		t.Errorf("--ccr-path did not reach the store: %v", err)
	}
}

// The estimator and tiktoken backends disagree on this body, so an unwired
// --model shows up as two identical token counts.
func TestProxyModelFlagReachesTheTokenizer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()

	tokensBefore := func(model string) int {
		t.Helper()
		t.Setenv("HEADROOM_CCR_BACKEND", "memory")
		base := runProxy(t, "--upstream", up.URL, "--model", model)

		req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(compressibleMessages()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", "sk-ant-api03-x")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)

		n, err := strconv.Atoi(resp.Header.Get("X-Headroom-Tokens-Before"))
		if err != nil {
			t.Fatalf("X-Headroom-Tokens-Before = %q: %v (compression did not run)",
				resp.Header.Get("X-Headroom-Tokens-Before"), err)
		}
		return n
	}

	if a, b := tokensBefore("claude"), tokensBefore("gpt-4o"); a == b {
		t.Errorf("--model did not reach the tokenizer: both models counted %d tokens", a)
	}
}

// compressibleMessages is an Anthropic /v1/messages body whose repetitive log
// payload the live-zone dispatcher reliably compresses.
func compressibleMessages() string {
	var b strings.Builder
	b.WriteString("FAILED build with 1 error\n")
	for i := 0; i < 300; i++ {
		b.WriteString("2026-01-01 00:00:00 INFO  worker: processed batch id=000000 status=ok latency_ms=12\n")
	}
	quoted, err := json.Marshal(b.String())
	if err != nil {
		panic(err)
	}
	return `{"model":"claude-3-5-sonnet-20241022","system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + string(quoted) + `}]}]}`
}

// runProxy starts "headroom proxy" for real on a loopback port and returns its
// base URL. The command is stopped when the test ends.
func runProxy(t *testing.T, args ...string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"proxy", "--listen", addr}, args...))
	var runErr error
	done := make(chan struct{})
	go func() {
		runErr = cmd.ExecuteContext(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if runErr != nil {
			t.Errorf("proxy exited with %v", runErr)
		}
	})

	base := "http://" + addr
	// 20s, not the 2s this used to allow. The old budget failed about 1 run in 6
	// once this package grew tests that start real subprocesses, because they
	// compete for the same machine while the proxy is coming up. A startup
	// timeout that scales with unrelated tests in the same package reports a
	// flake as a failure, and a suite people learn to re-run is a suite that
	// stops catching things.
	for i := 0; i < 2000; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			return base
		}
		select {
		case <-done:
			t.Fatalf("proxy exited instead of serving: %v", runErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("proxy never answered /healthz")
	return ""
}
