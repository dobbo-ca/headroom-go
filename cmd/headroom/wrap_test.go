package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/proxy"
)

// healthz serves one proxy.Health body, which is what probeProxy reads to
// decide whether it can trust an already-running proxy with this session.
func healthz(t *testing.T, h proxy.Health) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %q, want /healthz", r.URL.Path)
		}
		h.Status = "ok"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeAgent writes a shell script named agent onto a fresh PATH directory and
// returns (dir, outputFile).
func fakeAgent(t *testing.T, agent, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "invocation")
	if err := os.WriteFile(filepath.Join(dir, agent), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	// dir first, so the fake shadows any real agent; the system paths stay
	// so the script can still call `sleep`.
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("HEADROOM_TEST_OUT", out)
	return dir, out
}

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

// Every agent needs a default upstream, or `headroom wrap <agent>` is not one
// command: the user has to know HEADROOM_PROXY_UPSTREAM exists.
func TestEveryAgentHasADefaultUpstream(t *testing.T) {
	for name, spec := range agents {
		if spec.Upstream == "" {
			t.Errorf("agent %q has no default upstream", name)
		}
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
	if c.Upstream == x.Upstream {
		t.Error("claude and codex must use different default upstreams")
	}
}

func TestProxyHealthy(t *testing.T) {
	up := healthz(t, proxy.Health{Version: "test"})
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

// Something that answers 200 but is not a headroom proxy must not be trusted
// with the session: wrap would read a zero Health and silently believe replay
// is off.
func TestProxyHealthyRejectsNonJSON(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer up.Close()
	if proxyHealthy(http.DefaultClient, up.URL) {
		t.Error("a 200 that is not a headroom health body must not count as healthy")
	}
}

// probeProxy must surface the two settings wrap decides on.
func TestProbeProxyReportsReplayAndStore(t *testing.T) {
	up := healthz(t, proxy.Health{Version: "test", Replay: true, CCRPath: "/tmp/ccr.db"})
	h, ok := probeProxy(http.DefaultClient, up.URL)
	if !ok {
		t.Fatal("probeProxy did not read the health body")
	}
	if !h.Replay || h.CCRPath != "/tmp/ccr.db" {
		t.Errorf("probeProxy = %+v, want Replay true and the store path", h)
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
	up := healthz(t, proxy.Health{Version: "test"})
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
			p := healthz(t, proxy.Health{Version: "test", CCRPath: "/tmp/hr-test-ccr.db"})
			_, out := fakeAgent(t, tt.agent,
				"{ echo \"claude=$ANTHROPIC_BASE_URL\"; echo \"codex=$OPENAI_BASE_URL\"; "+
					"echo \"args=$*\"; } > \"$HEADROOM_TEST_OUT\"\n")
			t.Setenv("HEADROOM_PROXY_URL", p.URL)

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
			want := tt.agent + "=" + p.URL + tt.suffix
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
			if !strings.Contains(got, "--model opus") {
				t.Errorf("arguments were not passed through verbatim; got\n%s", got)
			}
		})
	}
}

// claude must receive an --mcp-config that launches `headroom mcp serve`
// against the SAME store the proxy reported. Without it the model sees
// <<ccr:HASH>> markers it cannot dereference.
func TestWrapGivesClaudeItsRetrievalTool(t *testing.T) {
	store := filepath.Join(t.TempDir(), "ccr.db")
	p := healthz(t, proxy.Health{Version: "test", CCRPath: store})
	_, out := fakeAgent(t, "claude", "echo \"$@\" > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "claude", "--", "-p", "hi"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap claude: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
	got := string(b)
	for _, want := range []string{"--mcp-config", "mcp", "serve", "--ccr-path", store, "--proxy-url", p.URL} {
		if !strings.Contains(got, want) {
			t.Errorf("claude args missing %q; got\n%s", want, got)
		}
	}
	// The user's own arguments still arrive.
	if !strings.Contains(got, "-p hi") {
		t.Errorf("user arguments were dropped; got\n%s", got)
	}
}

// Fail CLOSED: replay on and no way to wire retrieval means every marker on
// the wire stays unresolvable for the whole session, so refuse to start.
func TestWrapFailsClosedWhenReplayIsOnAndRetrievalCannotBeWired(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		store string
	}{
		{"agent takes no MCP flag", "codex", "/tmp/hr-test-ccr.db"},
		{"store is in memory", "claude", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := healthz(t, proxy.Health{Version: "test", Replay: true, CCRPath: tt.store})
			_, out := fakeAgent(t, tt.agent, "echo ran > \"$HEADROOM_TEST_OUT\"\n")
			t.Setenv("HEADROOM_PROXY_URL", p.URL)

			cmd := newRootCmd()
			cmd.SetArgs([]string{"wrap", tt.agent})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("wrap started the agent with replay on and no retrieval tool")
			}
			if !strings.Contains(err.Error(), "replay") {
				t.Errorf("the error must name replay, got %q", err)
			}
			if _, err := os.ReadFile(out); err == nil {
				t.Error("the agent ran even though wrap failed closed")
			}
		})
	}
}

// Fail OPEN: with replay off a marker survives a single turn, so a missing
// retrieval tool is a warning, not a refusal.
func TestWrapRunsWithoutRetrievalWhenReplayIsOff(t *testing.T) {
	p := healthz(t, proxy.Health{Version: "test", Replay: false, CCRPath: ""})
	_, out := fakeAgent(t, "codex", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap codex: %v", err)
	}
	if _, err := os.ReadFile(out); err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
}

// A proxy that is already healthy must be reused, not shadowed by a second
// listener of our own.
func TestWrapDoesNotStartASecondProxy(t *testing.T) {
	p := healthz(t, proxy.Health{Version: "test", CCRPath: "/tmp/hr-test-ccr.db"})
	_, out := fakeAgent(t, "claude", "echo ran > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)
	// If wrap ignored the running proxy it would try to bind this host, and
	// httptest already holds it, so ListenAndServe would fail loudly.
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://upstream.example")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := os.ReadFile(out); err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
}

// With nothing listening, wrap must run a proxy IN THIS PROCESS on the address
// it points the agent at, share its CCR store with the MCP server, and stop it
// when the agent exits.
func TestWrapStartsAndStopsItsOwnProxy(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ccr.db")
	gate := filepath.Join(dir, "gate")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// The agent parks until the test releases it, so the proxy is observable
	// while the session is "running".
	_, out := fakeAgent(t, "claude",
		"echo \"$@\" > \"$HEADROOM_TEST_OUT\"\n"+
			"while [ ! -f \"$HEADROOM_TEST_GATE\" ]; do sleep 0.05; done\n")
	t.Setenv("HEADROOM_TEST_GATE", gate)
	t.Setenv("HEADROOM_CCR_PATH", store)
	t.Setenv("HEADROOM_PROXY_UPSTREAM", upstream.URL)

	base := "http://" + freeAddr(t)
	t.Setenv("HEADROOM_PROXY_URL", base)

	done := make(chan error, 1)
	go func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"wrap", "claude"})
		done <- cmd.Execute()
	}()

	if err := waitForProxy(http.DefaultClient, base, 20*time.Second); err != nil {
		t.Fatalf("wrap never brought a proxy up at %s: %v", base, err)
	}
	h, ok := probeProxy(http.DefaultClient, base)
	if !ok || h.CCRPath != store {
		t.Fatalf("the proxy reports ccr_path %q, want %q — the MCP server would read a different store", h.CCRPath, store)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("wrap did not return after the agent exited")
	}

	// Teardown: the listener must be gone once the agent has exited.
	deadline := time.Now().Add(5 * time.Second)
	for proxyHealthy(http.DefaultClient, base) {
		if time.Now().After(deadline) {
			t.Fatal("the proxy outlived the agent")
		}
		time.Sleep(50 * time.Millisecond)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
	if !strings.Contains(string(b), store) {
		t.Errorf("the agent's MCP config does not name the shared store; got\n%s", b)
	}
}

// freeAddr returns a host:port nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// wrap must listen exactly where it points the agent, and must not need
// HEADROOM_PROXY_UPSTREAM to be set at all.
func TestProxyConfigForListensOnTheProxyURLHost(t *testing.T) {
	spec, _ := agentSpecFor("claude")
	t.Setenv("HEADROOM_PROXY_LISTEN", "0.0.0.0:9999")
	pcfg, err := proxyConfigFor(spec, "http://127.0.0.1:8787", "")
	if err != nil {
		t.Fatal(err)
	}
	if pcfg.Listen != "127.0.0.1:8787" {
		t.Errorf("Listen = %q, want the proxy URL host — a proxy nobody talks to is worse than none", pcfg.Listen)
	}
	if pcfg.Upstream != spec.Upstream {
		t.Errorf("Upstream = %q, want the agent default %q", pcfg.Upstream, spec.Upstream)
	}
}

// An explicit --upstream still wins over the agent default.
func TestProxyConfigForPrefersTheFlag(t *testing.T) {
	spec, _ := agentSpecFor("claude")
	pcfg, err := proxyConfigFor(spec, "http://127.0.0.1:8787", "https://gateway.example")
	if err != nil {
		t.Fatal(err)
	}
	if pcfg.Upstream != "https://gateway.example" {
		t.Errorf("Upstream = %q, want the flag value", pcfg.Upstream)
	}
}

func TestMCPFlags(t *testing.T) {
	claude, _ := agentSpecFor("claude")
	codex, _ := agentSpecFor("codex")

	args, blocked := mcpFlags(claude, "/tmp/ccr.db", "http://127.0.0.1:8787")
	if blocked != "" {
		t.Fatalf("claude was blocked: %s", blocked)
	}
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("args = %q", args)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[1]), &cfg); err != nil {
		t.Fatalf("the --mcp-config payload is not JSON: %v", err)
	}
	hr, ok := cfg.MCPServers["headroom"]
	if !ok {
		t.Fatal("no headroom server in the MCP config")
	}
	if strings.Join(hr.Args, " ") != "mcp serve --ccr-path /tmp/ccr.db --proxy-url http://127.0.0.1:8787" {
		t.Errorf("mcp serve args = %q", hr.Args)
	}

	if _, blocked := mcpFlags(codex, "/tmp/ccr.db", "http://x"); blocked == "" {
		t.Error("codex takes no inline MCP flag; mcpFlags must say so")
	}
	if _, blocked := mcpFlags(claude, "", "http://x"); blocked == "" {
		t.Error("an in-memory store cannot be shared across processes; mcpFlags must say so")
	}
}

// Agent flags must reach the agent without a "--" separator. One command
// means one command.
func TestWrapPassesAgentFlagsThroughWithoutASeparator(t *testing.T) {
	p := healthz(t, proxy.Health{Version: "test", CCRPath: "/tmp/hr-test-ccr.db"})
	_, out := fakeAgent(t, "claude", "echo \"$@\" > \"$HEADROOM_TEST_OUT\"\n")
	t.Setenv("HEADROOM_PROXY_URL", p.URL)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"wrap", "claude", "-p", "say ok", "--model", "sonnet"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap claude: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the agent was never executed: %v", err)
	}
	if got := string(b); !strings.Contains(got, "-p say ok") || !strings.Contains(got, "--model sonnet") {
		t.Errorf("agent flags did not reach the agent; got\n%s", got)
	}
}
