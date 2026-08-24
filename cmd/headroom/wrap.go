package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/spf13/cobra"
)

// agentSpec is everything headroom knows about one agent CLI.
type agentSpec struct {
	Binary  string
	EnvVar  string
	URLPath string
}

// agents is the supported set. Keep it small and explicit; adding one is a
// three-field table row, not a plugin system.
var agents = map[string]agentSpec{
	"claude": {Binary: "claude", EnvVar: "ANTHROPIC_BASE_URL", URLPath: ""},
	"codex":  {Binary: "codex", EnvVar: "OPENAI_BASE_URL", URLPath: "/v1"},
}

func agentSpecFor(name string) (agentSpec, bool) {
	s, ok := agents[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

func supportedAgents() string { return "claude, codex" }

// agentBaseURL is the value the agent's env var receives.
func agentBaseURL(spec agentSpec, base string) string {
	return strings.TrimRight(base, "/") + spec.URLPath
}

// proxyHealthy reports whether a headroom proxy answers at baseURL.
func proxyHealthy(client *http.Client, baseURL string) bool {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
		Long: "Starts the headroom proxy if it is not already running, points the\n" +
			"agent's base URL at it, and launches the agent.\n\nSupported agents: " +
			supportedAgents(),
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

			if !proxyHealthy(client, base) {
				if err := spawnProxy(upstream); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "headroom: started proxy at %s\n", base)
				if err := waitForProxy(client, base, 45*time.Second); err != nil {
					return err
				}
			}

			bin, err := exec.LookPath(spec.Binary)
			if err != nil {
				return fmt.Errorf("cannot find %q on PATH: %w", spec.Binary, err)
			}

			agentCmd := exec.Command(bin, args[1:]...)
			agentCmd.Env = append(os.Environ(), spec.EnvVar+"="+agentBaseURL(spec, base))
			agentCmd.Stdin, agentCmd.Stdout, agentCmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return agentCmd.Run()
		},
	}

	cmd.Flags().StringVar(&upstream, "upstream", "",
		"upstream API base URL for a proxy this command starts (env HEADROOM_PROXY_UPSTREAM)")
	return cmd
}

// spawnProxy starts `headroom proxy` as a detached child using this same
// executable, so the wrapped agent does not need headroom on its PATH. It is a
// variable so tests can exercise the start-the-proxy branch without spawning a
// second process.
var spawnProxy = func(upstream string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the headroom executable: %w", err)
	}
	args := []string{"proxy"}
	if upstream != "" {
		args = append(args, "--upstream", upstream)
	}
	proc := exec.Command(self, args...)
	proc.Env = os.Environ()
	proc.Stdout, proc.Stderr = os.Stderr, os.Stderr
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start the headroom proxy: %w", err)
	}
	return nil
}
