package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/proxy"
)

// stderrMu serializes tests that redirect os.Stderr to avoid data races.
var stderrMu sync.Mutex

// GUARD 1: Bedrock routing bypasses the base URL, so warn when it will.

func TestWrapWarnsBedrock(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	p := healthz(t, proxy.Health{Version: "test", CCRPath: store})
	_, _ = fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")

	stderr := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("wrap refused to start with Bedrock set: %v (must warn, not refuse)", err)
		}
	})

	if !strings.Contains(stderr, "WARNING: CLAUDE_CODE_USE_BEDROCK is set") {
		t.Errorf("Guard 1 did not warn about BEDROCK; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "unset CLAUDE_CODE_USE_BEDROCK or set it to 0") {
		t.Errorf("Guard 1 did not suggest remediation; stderr:\n%s", stderr)
	}
}

func TestWrapWarnsVertex(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	p := healthz(t, proxy.Health{Version: "test", CCRPath: store})
	_, _ = fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "") // Unset so else-if reaches VERTEX.
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")

	stderr := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("wrap refused to start with Vertex set: %v (must warn, not refuse)", err)
		}
	})

	if !strings.Contains(stderr, "WARNING: CLAUDE_CODE_USE_VERTEX is set") {
		t.Errorf("Guard 1 did not warn about VERTEX; stderr:\n%s", stderr)
	}
}

func TestWrapNoWarnWhenNeitherBedrockNorVertexSet(t *testing.T) {
	p := healthz(t, proxy.Health{Version: "test", CCRPath: "/tmp/hr-test-ccr.db"})
	_, out := fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	// Explicitly NOT setting CLAUDE_CODE_USE_BEDROCK or CLAUDE_CODE_USE_VERTEX.
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := os.ReadFile(out); err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
}

func TestWrapBypassOverrideSuppressesWarning(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	p := healthz(t, proxy.Health{Version: "test", CCRPath: store})
	_, _ = fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("HEADROOM_BYPASS_OK", "1")

	stderr := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("wrap: %v", err)
		}
	})

	if strings.Contains(stderr, "WARNING: CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("BYPASS_OK=1 did not suppress Guard 1; stderr:\n%s", stderr)
	}
}

func TestWrapNoWarnOnBedrockZero(t *testing.T) {
	// BEDROCK=0 is the documented fix and must not warn.
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	p := healthz(t, proxy.Health{Version: "test", CCRPath: store})
	_, _ = fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "0")

	stderr := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("wrap: %v", err)
		}
	})

	if strings.Contains(stderr, "WARNING: CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("BEDROCK=0 (the documented fix) triggered Guard 1; stderr:\n%s", stderr)
	}
}

// GUARD 2: After the agent exits, if zero requests reached the proxy, warn.

func TestWrapWarnsZeroRequests(t *testing.T) {
	stderrMu.Lock()

	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	gate := filepath.Join(dir, "gate")
	stderrFile := filepath.Join(dir, "stderr")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, _ = fakeAgent(t, "claude",
		"while [ ! -f \"$HEADROOM_TEST_GATE\" ]; do sleep 0.05; done\n")
	t.Setenv("HEADROOM_TEST_GATE", gate)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("HEADROOM_PROXY_UPSTREAM", upstream.URL)

	base := "http://" + freeAddr(t)
	t.Setenv("HEADROOM_PROXY_URL", base)

	done := make(chan error, 1)
	go func() {
		oldStderr := os.Stderr
		f, _ := os.Create(stderrFile)
		os.Stderr = f
		defer func() {
			f.Close()
			os.Stderr = oldStderr
		}()

		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		done <- cmd.Execute()
	}()

	if err := waitForProxy(http.DefaultClient, base, 20*time.Second); err != nil {
		stderrMu.Unlock()
		t.Fatalf("proxy never came up: %v", err)
	}

	// Agent exits without making any requests. Release the gate.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		stderrMu.Unlock()
		t.Fatal(err)
	}

	select {
	case err := <-done:
		stderrMu.Unlock()
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		time.Sleep(100 * time.Millisecond) // Let stderr file flush.
		out, _ := os.ReadFile(stderrFile)
		stderr := string(out)
		if !strings.Contains(stderr, "WARNING: The agent exited but sent ZERO requests") {
			t.Errorf("Guard 2 did not warn on zero requests; stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "unset it or set it to 0") {
			t.Errorf("Guard 2 did not suggest remediation; stderr:\n%s", stderr)
		}
	case <-time.After(20 * time.Second):
		stderrMu.Unlock()
		t.Fatal("wrap did not return after the agent exited")
	}
}

func TestWrapNoWarnWhenRequestsReachedProxy(t *testing.T) {
	stderrMu.Lock()

	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	gate := filepath.Join(dir, "gate")
	stderrFile := filepath.Join(dir, "stderr")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message"}`))
	}))
	defer upstream.Close()

	_, _ = fakeAgent(t, "claude",
		"curl -s -X POST \"$ANTHROPIC_BASE_URL/v1/messages\" "+
			"-H \"Content-Type: application/json\" "+
			"-d '{\"model\":\"claude-3-5-sonnet-20241022\",\"max_tokens\":10,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}' "+
			"> /dev/null\n"+
			"while [ ! -f \"$HEADROOM_TEST_GATE\" ]; do sleep 0.05; done\n")
	t.Setenv("HEADROOM_TEST_GATE", gate)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("HEADROOM_PROXY_UPSTREAM", upstream.URL)

	base := "http://" + freeAddr(t)
	t.Setenv("HEADROOM_PROXY_URL", base)

	done := make(chan error, 1)
	go func() {
		oldStderr := os.Stderr
		f, _ := os.Create(stderrFile)
		os.Stderr = f
		defer func() {
			f.Close()
			os.Stderr = oldStderr
		}()

		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		done <- cmd.Execute()
	}()

	if err := waitForProxy(http.DefaultClient, base, 20*time.Second); err != nil {
		stderrMu.Unlock()
		t.Fatalf("proxy never came up: %v", err)
	}

	// Wait a moment for the agent to make its request.
	time.Sleep(500 * time.Millisecond)

	// Release the gate.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		stderrMu.Unlock()
		t.Fatal(err)
	}

	select {
	case err := <-done:
		stderrMu.Unlock()
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		out, _ := os.ReadFile(stderrFile)
		stderr := string(out)
		if strings.Contains(stderr, "WARNING: The agent exited but sent ZERO requests") {
			t.Errorf("Guard 2 warned on non-zero requests; stderr:\n%s", stderr)
		}
	case <-time.After(20 * time.Second):
		stderrMu.Unlock()
		t.Fatal("wrap did not return after the agent exited")
	}
}

// An uncompressed request (no_live_zone or no_candidates) must still count as a
// request. The counter must count arrivals, not ledger writes or applied compressions.
func TestWrapCountsUncompressedRequests(t *testing.T) {
	stderrMu.Lock()

	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	gate := filepath.Join(dir, "gate")
	stderrFile := filepath.Join(dir, "stderr")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message"}`))
	}))
	defer upstream.Close()

	// POST to /v1/messages with compression OFF, so handleForward is reached but
	// no compression is applied.
	_, _ = fakeAgent(t, "claude",
		"curl -s -X POST \"$ANTHROPIC_BASE_URL/v1/messages\" "+
			"-H \"Content-Type: application/json\" "+
			"-d '{\"model\":\"claude-3-5-sonnet-20241022\",\"max_tokens\":10,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}' "+
			"> /dev/null\n"+
			"while [ ! -f \"$HEADROOM_TEST_GATE\" ]; do sleep 0.05; done\n")
	t.Setenv("HEADROOM_TEST_GATE", gate)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("HEADROOM_PROXY_UPSTREAM", upstream.URL)
	t.Setenv("HEADROOM_PROXY_COMPRESS", "off") // Ensure no compression happens.

	base := "http://" + freeAddr(t)
	t.Setenv("HEADROOM_PROXY_URL", base)

	done := make(chan error, 1)
	go func() {
		oldStderr := os.Stderr
		f, _ := os.Create(stderrFile)
		os.Stderr = f
		defer func() {
			f.Close()
			os.Stderr = oldStderr
		}()

		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		done <- cmd.Execute()
	}()

	if err := waitForProxy(http.DefaultClient, base, 20*time.Second); err != nil {
		stderrMu.Unlock()
		t.Fatalf("proxy never came up: %v", err)
	}

	// Wait for the POST to complete.
	time.Sleep(500 * time.Millisecond)

	// Release the gate.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		stderrMu.Unlock()
		t.Fatal(err)
	}

	select {
	case err := <-done:
		stderrMu.Unlock()
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		out, _ := os.ReadFile(stderrFile)
		stderr := string(out)
		if strings.Contains(stderr, "WARNING: The agent exited but sent ZERO requests") {
			t.Errorf("Guard 2 warned on uncompressed request that should have been counted; stderr:\n%s", stderr)
		}
	case <-time.After(20 * time.Second):
		stderrMu.Unlock()
		t.Fatal("wrap did not return after the agent exited")
	}
}

func TestWrapWarnsZeroRequestsEvenOnNonZeroExit(t *testing.T) {
	// Guard 2 must warn even if the agent exits non-zero (a common failure case).
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, _ = fakeAgent(t, "claude", "exit 1\n")
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("HEADROOM_PROXY_UPSTREAM", upstream.URL)

	base := "http://" + freeAddr(t)
	t.Setenv("HEADROOM_PROXY_URL", base)

	stderr := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		_ = cmd.Execute() // Expect error, don't check it.
	})

	if !strings.Contains(stderr, "WARNING: The agent exited but sent ZERO requests") {
		t.Errorf("Guard 2 did not warn on zero requests with non-zero exit; stderr:\n%s", stderr)
	}
}

// captureStderr runs fn with stderr redirected to a file and returns the captured text.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderrMu.Lock()
	defer stderrMu.Unlock()

	stderrFile := filepath.Join(t.TempDir(), "stderr")
	oldStderr := os.Stderr
	f, err := os.Create(stderrFile)
	if err != nil {
		t.Fatalf("create stderr file: %v", err)
	}
	os.Stderr = f

	fn()

	f.Close()
	os.Stderr = oldStderr

	out, err := os.ReadFile(stderrFile)
	if err != nil {
		t.Fatalf("read stderr file: %v", err)
	}
	return string(out)
}
