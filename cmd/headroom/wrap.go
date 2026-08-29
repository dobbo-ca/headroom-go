package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/dobbo-ca/headroom-go/internal/proxy"
	"github.com/spf13/cobra"
)

// agentSpec is everything headroom knows about one agent CLI.
type agentSpec struct {
	Binary  string
	EnvVar  string
	URLPath string
	// Upstream is the API this agent talks to, used only when neither
	// --upstream nor HEADROOM_PROXY_UPSTREAM says otherwise. Without it
	// `headroom wrap claude` is not one command, it is two.
	Upstream string
	// MCPConfigFlag is the flag this agent accepts an inline MCP server
	// definition on. Empty means headroom cannot hand this agent its
	// retrieval tool, so the model cannot dereference a <<ccr:HASH>>.
	MCPConfigFlag string
}

// agents is the supported set. Keep it small and explicit; adding one is a
// table row, not a plugin system.
var agents = map[string]agentSpec{
	"claude": {
		Binary: "claude", EnvVar: "ANTHROPIC_BASE_URL", URLPath: "",
		Upstream: "https://api.anthropic.com", MCPConfigFlag: "--mcp-config",
	},
	"codex": {
		Binary: "codex", EnvVar: "OPENAI_BASE_URL", URLPath: "/v1",
		Upstream: "https://api.openai.com",
	},
}

func agentSpecFor(name string) (agentSpec, bool) {
	s, ok := agents[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

func supportedAgents() string { return "claude, codex" }

// isTruthy reports whether s is a truthy value (1, true, yes, on).
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// agentBaseURL is the value the agent's env var receives.
func agentBaseURL(spec agentSpec, base string) string {
	return strings.TrimRight(base, "/") + spec.URLPath
}

// probeProxy reads /healthz. The second return is false when no headroom
// proxy answers at baseURL.
func probeProxy(client *http.Client, baseURL string) (proxy.Health, bool) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/healthz")
	if err != nil {
		return proxy.Health{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proxy.Health{}, false
	}
	var h proxy.Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return proxy.Health{}, false
	}
	return h, true
}

// proxyHealthy reports whether a headroom proxy answers at baseURL.
func proxyHealthy(client *http.Client, baseURL string) bool {
	_, ok := probeProxy(client, baseURL)
	return ok
}

// waitForProxy polls /healthz until it answers or timeout elapses.
func waitForProxy(client *http.Client, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if proxyHealthy(client, baseURL) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy at %s did not become healthy within %s", baseURL, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func newWrapCmd() *cobra.Command {
	var upstream string

	cmd := &cobra.Command{
		Use:   "wrap <agent> [args...]",
		Short: "Run an agent CLI through the headroom proxy",
		Long: "Starts the headroom proxy, points the agent's base URL at it, gives the\n" +
			"agent a headroom MCP server on the same CCR store, and launches the agent.\n" +
			"Both stop when the agent exits.\n\nSupported agents: " + supportedAgents(),
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, ok := agentSpecFor(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q; supported agents are %s", args[0], supportedAgents())
			}

			cfg, err := config.Load(config.Overrides{})
			if err != nil {
				return err
			}
			base := cfg.ProxyURL
			client := &http.Client{Timeout: 2 * time.Second}

			// GUARD 1: Claude Code routes requests when BEDROCK or VERTEX is set,
			// bypassing the base URL env var. Warn the user that the proxy will see
			// nothing (bead hr-sw9). The flag HEADROOM_BYPASS_OK=1 silences this
			// because a false measurement is less hostile than a hard exit.
			if !isTruthy(os.Getenv("HEADROOM_BYPASS_OK")) {
				bypass := ""
				if isTruthy(os.Getenv("CLAUDE_CODE_USE_BEDROCK")) {
					bypass = "CLAUDE_CODE_USE_BEDROCK"
				} else if isTruthy(os.Getenv("CLAUDE_CODE_USE_VERTEX")) {
					bypass = "CLAUDE_CODE_USE_VERTEX"
				}
				if bypass != "" {
					fmt.Fprintf(os.Stderr, "\n"+
						"headroom: WARNING: %s is set. The agent will BYPASS the proxy.\n"+
						"          %s is ignored when this is set.\n"+
						"          The ledger will show zero requests. Every measurement will be wrong.\n"+
						"          To fix: unset %s or set it to 0.\n"+
						"          To suppress this warning: HEADROOM_BYPASS_OK=1\n\n", bypass, spec.EnvVar, bypass)
				}
			}

			// storePath and replayOn must describe the proxy that will
			// actually serve this session, which is not always the one
			// this process would have configured.
			storePath, replayOn := cfg.CCRPath, false

			// ownProxy is set when wrap starts its own proxy (not reusing an
			// existing one). Guard 2 checks its RequestCount after the agent exits.
			var ownProxy *proxy.Server

			if h, running := probeProxy(client, base); running {
				storePath, replayOn = h.CCRPath, h.Replay
				fmt.Fprintf(os.Stderr, "headroom: reusing the proxy already listening at %s\n", base)
			} else {
				pcfg, err := proxyConfigFor(spec, base, upstream)
				if err != nil {
					return err
				}
				replayOn = pcfg.Replay
				srv, stop, err := startProxy(cmd.Context(), pcfg, cfg)
				if err != nil {
					return err
				}
				ownProxy = srv
				// The proxy is a goroutine in THIS process, so it cannot
				// outlive the agent and cannot die behind its back.
				defer stop()
				if err := waitForProxy(client, base, 45*time.Second); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "headroom: proxy listening on %s -> %s\n", pcfg.Listen, pcfg.Upstream)
			}

			mcpArgs, blocked := mcpFlags(spec, storePath, base)
			switch {
			case blocked == "":
				fmt.Fprintf(os.Stderr, "headroom: MCP retrieval wired to %s\n", storePath)
			case replayOn:
				// Fail CLOSED. With replay on, every marker headroom
				// writes stays on the wire for the rest of the session,
				// so an agent that cannot dereference one is blind for
				// the whole session rather than for a single turn.
				return fmt.Errorf("replay is on but headroom cannot give %s its retrieval tool: %s\n"+
					"The model would see <<ccr:HASH>> markers it cannot resolve for the whole session.\n"+
					"Set HEADROOM_PROXY_REPLAY=off to run without replay", spec.Binary, blocked)
			default:
				// Fail OPEN. Without replay a marker survives one turn.
				fmt.Fprintf(os.Stderr,
					"headroom: warning: no retrieval tool for %s: %s\n", spec.Binary, blocked)
			}

			bin, err := exec.LookPath(spec.Binary)
			if err != nil {
				return fmt.Errorf("cannot find %q on PATH: %w", spec.Binary, err)
			}

			agentCmd := exec.Command(bin, append(mcpArgs, args[1:]...)...)
			// Override bypass vars to 0 so the agent does not route around the proxy.
			childEnv := append(os.Environ(), spec.EnvVar+"="+agentBaseURL(spec, base))
			if isTruthy(os.Getenv("CLAUDE_CODE_USE_BEDROCK")) {
				childEnv = append(childEnv, "CLAUDE_CODE_USE_BEDROCK=0")
			}
			if isTruthy(os.Getenv("CLAUDE_CODE_USE_VERTEX")) {
				childEnv = append(childEnv, "CLAUDE_CODE_USE_VERTEX=0")
			}
			agentCmd.Env = childEnv
			agentCmd.Stdin, agentCmd.Stdout, agentCmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			agentErr := agentCmd.Run()

			// GUARD 2: The agent exited having sent zero requests to the proxy we
			// started. This catches a bypassed proxy (the Bedrock/Vertex routing
			// Guard 1 warns about), a typo'd env var, or an agent that never
			// started. When reusing an existing proxy we cannot check this, because
			// we do not own that server and its RequestCount includes other sessions
			// (bead hr-sw9).
			if ownProxy != nil && ownProxy.RequestCount() == 0 {
				fmt.Fprintf(os.Stderr, "\n"+
					"headroom: WARNING: The agent exited but sent ZERO requests through the proxy.\n"+
					"          Possible causes:\n"+
					"            - A routing env var is set (CLAUDE_CODE_USE_BEDROCK, CLAUDE_CODE_USE_VERTEX)\n"+
					"              -> unset it or set it to 0\n"+
					"            - The base URL env var (%s) was overridden elsewhere\n"+
					"              -> remove the override\n"+
					"            - The agent never started (check the exit status above)\n"+
					"          The ledger is empty.\n\n", spec.EnvVar)
			}

			if agentErr != nil {
				return agentErr
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&upstream, "upstream", "",
		"upstream API base URL for a proxy this command starts (env HEADROOM_PROXY_UPSTREAM)")
	// Everything after the agent name belongs to the agent. Without this,
	// `headroom wrap claude -p 'say ok'` dies on wrap's own flag parser and
	// the user has to learn to type a "--" separator.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// proxyConfigFor resolves the configuration for a proxy wrap starts itself.
//
// The listen address comes from HEADROOM_PROXY_URL rather than from
// HEADROOM_PROXY_LISTEN, because wrap has to listen exactly where it points
// the agent. Honouring both would let the two settings disagree and produce a
// proxy nobody talks to.
func proxyConfigFor(spec agentSpec, base, upstream string) (proxy.Config, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return proxy.Config{}, fmt.Errorf("wrap: proxy URL %q has no host", base)
	}
	if upstream == "" && os.Getenv("HEADROOM_PROXY_UPSTREAM") == "" {
		upstream = spec.Upstream
	}
	return proxy.Load(proxy.Overrides{Upstream: upstream, Listen: u.Host})
}

// startProxy runs the proxy in this process and returns the Server and a
// function that stops it and waits for the listener to drain. The Server is
// returned so wrap can check RequestCount after the agent exits (Guard 2,
// bead hr-sw9).
func startProxy(ctx context.Context, pcfg proxy.Config, cfg config.Config) (*proxy.Server, func(), error) {
	srv, err := newProxyServer(pcfg, cfg)
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.ListenAndServe(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "headroom: proxy stopped:", err)
		}
	}()
	return srv, func() { cancel(); <-done }, nil
}

// mcpFlags builds the agent flags that point it at `headroom mcp serve` on
// storePath. The second return is empty on success and otherwise says, in one
// clause, why headroom cannot wire the tool.
func mcpFlags(spec agentSpec, storePath, proxyURL string) (args []string, blocked string) {
	if spec.MCPConfigFlag == "" {
		return nil, fmt.Sprintf("%s takes no inline MCP configuration flag", spec.Binary)
	}
	if storePath == "" {
		return nil, "the CCR store is in memory, so the proxy and the MCP server cannot share it " +
			"(set HEADROOM_CCR_BACKEND=sqlite)"
	}
	self, err := os.Executable()
	if err != nil {
		return nil, "cannot locate the headroom executable: " + err.Error()
	}
	blob, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"headroom": map[string]any{
		"command": self,
		"args":    []string{"mcp", "serve", "--ccr-path", storePath, "--proxy-url", proxyURL},
	}}})
	if err != nil {
		return nil, "encode the MCP configuration: " + err.Error()
	}
	return []string{spec.MCPConfigFlag, string(blob)}, ""
}
