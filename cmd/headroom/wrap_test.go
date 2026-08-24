package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentSpecFor(t *testing.T) {
	tests := []struct {
		name    string
		binary  string
		envVar  string
		urlPath string
		ok      bool
	}{
		{"claude", "claude", "ANTHROPIC_BASE_URL", "", true},
		{"codex", "codex", "OPENAI_BASE_URL", "/v1", true},
		{"CLAUDE", "claude", "ANTHROPIC_BASE_URL", "", true},
		{"cursor", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := agentSpecFor(tt.name)
			if ok != tt.ok {
				t.Fatalf("agentSpecFor(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.Binary != tt.binary || got.EnvVar != tt.envVar || got.URLPath != tt.urlPath {
				t.Errorf("agentSpecFor(%q) = %+v", tt.name, got)
			}
		})
	}
}

// claude and codex must not share an env var; swapping them would silently
// point one agent at the wrong protocol.
func TestAgentSpecsAreDistinct(t *testing.T) {
	c, _ := agentSpecFor("claude")
	x, _ := agentSpecFor("codex")
	if c.EnvVar == x.EnvVar {
		t.Error("claude and codex must use different base-URL environment variables")
	}
	if c.URLPath == x.URLPath {
		t.Error("claude and codex must use different URL paths")
	}
}

func TestProxyHealthy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %q, want /healthz", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	if !proxyHealthy(http.DefaultClient, up.URL) {
		t.Error("a healthy proxy was reported unhealthy")
	}
	if proxyHealthy(http.DefaultClient, "http://127.0.0.1:1") {
		t.Error("an unreachable proxy was reported healthy")
	}
}

// A non-200 from /healthz is not healthy.
func TestProxyHealthyRejectsNon200(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer up.Close()
	if proxyHealthy(http.DefaultClient, up.URL) {
		t.Error("a 503 must not count as healthy")
	}
}

func TestWaitForProxyTimesOut(t *testing.T) {
	start := time.Now()
	// Off the test goroutine, so a waitForProxy that never gives up fails here
	// in seconds instead of hanging the package until the go test timeout.
	done := make(chan error, 1)
	go func() { done <- waitForProxy(http.DefaultClient, "http://127.0.0.1:1", 250*time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForProxy ignored its timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("waitForProxy took %v; it must respect its timeout", elapsed)
	}
}

func TestWaitForProxySucceeds(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	if err := waitForProxy(http.DefaultClient, up.URL, 2*time.Second); err != nil {
		t.Errorf("waitForProxy failed against a healthy proxy: %v", err)
	}
}

func TestWrapRejectsUnknownAgent(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "emacs"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
	if !strings.Contains(err.Error(), "emacs") {
		t.Errorf("error must name the agent, got %q", err)
	}
	// The error should list what IS supported.
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "codex") {
		t.Errorf("error must list the supported agents, got %q", err)
	}
}

func TestWrapRequiresAnAgent(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error with no agent named")
	}
}

// The env var the agent receives must carry the codex /v1 suffix and no
// double slash.
func TestAgentBaseURL(t *testing.T) {
	tests := []struct {
		agent string
		base  string
		want  string
	}{
		{"claude", "http://127.0.0.1:8787", "http://127.0.0.1:8787"},
		{"claude", "http://127.0.0.1:8787/", "http://127.0.0.1:8787"},
		{"codex", "http://127.0.0.1:8787", "http://127.0.0.1:8787/v1"},
		{"codex", "http://127.0.0.1:8787/", "http://127.0.0.1:8787/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.agent+" "+tt.base, func(t *testing.T) {
			spec, ok := agentSpecFor(tt.agent)
			if !ok {
				t.Fatal("unknown agent")
			}
			if got := agentBaseURL(spec, tt.base); got != tt.want {
				t.Errorf("agentBaseURL = %q, want %q", got, tt.want)
			}
		})
	}
}

// The exec path: with a healthy proxy already listening, wrap must launch the
// agent binary from PATH, hand it ONLY its own agent's base-URL variable set
// to the right value, and pass the remaining arguments through unchanged.
func TestWrapRunsTheAgentWithItsBaseURL(t *testing.T) {
	tests := []struct {
		agent   string
		envVar  string
		suffix  string
		otherEV string
	}{
		{"claude", "ANTHROPIC_BASE_URL", "", "OPENAI_BASE_URL"},
		{"codex", "OPENAI_BASE_URL", "/v1", "ANTHROPIC_BASE_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Errorf("probed %q, want /healthz", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer proxy.Close()

			dir := t.TempDir()
			out := filepath.Join(dir, "invocation")
			script := "#!/bin/sh\n{ echo \"claude=$ANTHROPIC_BASE_URL\"; echo \"codex=$OPENAI_BASE_URL\"; " +
				"echo \"args=$*\"; } > \"$HEADROOM_TEST_OUT\"\n"
			if err := os.WriteFile(filepath.Join(dir, tt.agent), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			t.Setenv("HEADROOM_PROXY_URL", proxy.URL)
			t.Setenv("PATH", dir)
			t.Setenv("HEADROOM_TEST_OUT", out)

			cmd := newRootCmd()
			cmd.SetArgs([]string{"wrap", tt.agent, "--", "--model", "opus"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("wrap %s: %v", tt.agent, err)
			}

			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("the agent was never executed: %v", err)
			}
			got := string(b)
			want := tt.agent + "=" + proxy.URL + tt.suffix
			if !strings.Contains(got, want+"\n") {
				t.Errorf("agent env: got\n%s\nwant a line %q", got, want)
			}
			// The other agent's variable must stay empty: wrap sets exactly one.
			otherKey := "codex="
			if tt.agent == "codex" {
				otherKey = "claude="
			}
			if !strings.Contains(got, otherKey+"\n") {
				t.Errorf("wrap set %s as well; got\n%s", tt.otherEV, got)
			}
			if !strings.Contains(got, "args=--model opus\n") {
				t.Errorf("arguments were not passed through verbatim; got\n%s", got)
			}
		})
	}
}

// When the proxy is down, wrap must start one, pass --upstream through to it,
// wait for it to answer, and only then launch the agent.
func TestWrapStartsTheProxyWhenItIsDown(t *testing.T) {
	var healthy atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxy.Close()

	spawned := make(chan string, 4)
	orig := spawnProxy
	t.Cleanup(func() { spawnProxy = orig })
	spawnProxy = func(upstream string) error {
		spawned <- upstream
		healthy.Store(true)
		return nil
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "invocation")
	script := "#!/bin/sh\necho ran > \"$HEADROOM_TEST_OUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEADROOM_PROXY_URL", proxy.URL)
	t.Setenv("PATH", dir)
	t.Setenv("HEADROOM_TEST_OUT", out)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "--upstream", "https://upstream.example", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap: %v", err)
	}

	select {
	case got := <-spawned:
		if got != "https://upstream.example" {
			t.Errorf("spawnProxy got upstream %q, want the flag value", got)
		}
	default:
		t.Fatal("wrap did not start a proxy when /healthz was failing")
	}
	if len(spawned) != 0 {
		t.Errorf("wrap started %d extra proxies", len(spawned))
	}
	if _, err := os.ReadFile(out); err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
}

// A proxy that is already healthy must not be started a second time.
func TestWrapDoesNotStartASecondProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	orig := spawnProxy
	t.Cleanup(func() { spawnProxy = orig })
	started := 0
	spawnProxy = func(string) error { started++; return nil }

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"),
		[]byte("#!/bin/sh\necho ran > \"$HEADROOM_TEST_OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEADROOM_PROXY_URL", proxy.URL)
	t.Setenv("PATH", dir)
	t.Setenv("HEADROOM_TEST_OUT", filepath.Join(dir, "invocation"))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if started != 0 {
		t.Errorf("wrap started %d proxies against a healthy one", started)
	}
}
