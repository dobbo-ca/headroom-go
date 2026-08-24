package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	err := waitForProxy(http.DefaultClient, "http://127.0.0.1:1", 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
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
