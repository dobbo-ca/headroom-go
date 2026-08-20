package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootReportsVersion(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("--version output %q does not contain %q", out.String(), version)
	}
}

func TestRootHasMCPServeSubcommand(t *testing.T) {
	root := newRootCmd()
	mcpCmd, _, err := root.Find([]string{"mcp"})
	if err != nil || mcpCmd.Name() != "mcp" {
		t.Fatalf("no mcp subcommand: cmd=%v err=%v", mcpCmd, err)
	}
	serve, _, err := root.Find([]string{"mcp", "serve"})
	if err != nil || serve.Name() != "serve" {
		t.Fatalf("no mcp serve subcommand: cmd=%v err=%v", serve, err)
	}
}

// The MCP protocol owns stdout. Cobra must never write to it.
func TestRootWritesDiagnosticsToStderrNotStdout(t *testing.T) {
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"mcp", "serve", "--ccr-backend", "cassandra"})
	if err := root.Execute(); err == nil {
		t.Fatal("an invalid backend was accepted")
	}
	if stdout.Len() != 0 {
		t.Errorf("cobra wrote %q to stdout; the MCP stream must stay clean", stdout.String())
	}
}

func TestServeFlagsExist(t *testing.T) {
	root := newRootCmd()
	serve, _, err := root.Find([]string{"mcp", "serve"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	for _, name := range []string{"ccr-backend", "ccr-path", "proxy-url", "model"} {
		if serve.Flags().Lookup(name) == nil {
			t.Errorf("mcp serve is missing the --%s flag", name)
		}
	}
}

// newRootCmd must wire both streams to stderr before any caller overrides
// them, otherwise a real "mcp serve" run corrupts the protocol stream.
func TestRootDefaultsBothStreamsToStderr(t *testing.T) {
	root := newRootCmd()
	if root.OutOrStdout() != os.Stderr {
		t.Error("root out stream is not os.Stderr")
	}
	if root.ErrOrStderr() != os.Stderr {
		t.Error("root err stream is not os.Stderr")
	}
}

// runServe executes "mcp serve" with stdin holding the given JSON-RPC lines and
// then closed, so ServeStdio reaches EOF and returns. It reports everything
// written to the real os.Stdout while it ran, plus the command error.
func runServe(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	io.WriteString(inW, stdin)
	inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, outR)
		captured <- buf.String()
	}()

	root := newRootCmd()
	root.SetArgs(append([]string{"mcp", "serve"}, args...))
	runErr := root.Execute()

	os.Stdin, os.Stdout = oldIn, oldOut
	outW.Close()
	inR.Close()
	out := <-captured
	outR.Close()
	return out, runErr
}

// The store backends must be registered, or every serve run dies with
// "backend not registered".
func TestServeStartsWithMemoryBackend(t *testing.T) {
	t.Setenv("HEADROOM_CCR_BACKEND", "memory")
	stdout, err := runServe(t, "")
	if err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	if stdout != "" {
		t.Errorf("serve wrote %q to stdout with no requests", stdout)
	}
}

// serve must create the CCR directory before opening the SQLite store.
func TestServeCreatesCCRDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "no", "such", "dir")
	t.Setenv("HEADROOM_HOME", home)
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")
	if _, err := runServe(t, ""); err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "ccr.db")); err != nil {
		t.Errorf("CCR store was not created under HEADROOM_HOME: %v", err)
	}
}

// The initialize round trip must land on stdout as JSON-RPC and nothing else,
// carrying the CLI's version in serverInfo.
func TestServeAnswersInitializeOnStdoutOnly(t *testing.T) {
	t.Setenv("HEADROOM_CCR_BACKEND", "memory")
	const req = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n"
	stdout, err := runServe(t, req)
	if err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout carried %d lines, want exactly the one JSON-RPC reply: %q", len(lines), stdout)
	}
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("stdout line is not JSON-RPC: %v (%q)", err, lines[0])
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", resp.JSONRPC)
	}
	if resp.Result.ServerInfo.Version != version {
		t.Errorf("serverInfo.version = %q, want the CLI version %q", resp.Result.ServerInfo.Version, version)
	}
}
