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

// Each serve flag must reach config.Load, not merely exist. Asserting that
// Flags().Lookup finds a flag proves nothing: a flag bound to a throwaway
// variable still looks present while doing nothing. Every case below fails if
// its StringVar target is disconnected.

func TestServeCCRPathFlagIsWired(t *testing.T) {
	// HEADROOM_HOME points somewhere else, so only the flag can put the store
	// at the path we assert on.
	t.Setenv("HEADROOM_HOME", t.TempDir())
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")
	want := filepath.Join(t.TempDir(), "nested", "custom.db")

	if _, err := runServe(t, "", "--ccr-path", want); err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("--ccr-path did not reach the store: %v", err)
	}
}

func TestServeProxyURLFlagIsWired(t *testing.T) {
	t.Setenv("HEADROOM_CCR_BACKEND", "memory")
	t.Setenv("HEADROOM_PROXY_URL", "http://valid-from-env:8787")

	_, err := runServe(t, "", "--proxy-url", "ftp://rejected")
	if err == nil {
		t.Fatal("--proxy-url did not reach config validation; the bad scheme was accepted")
	}
	if !strings.Contains(err.Error(), "HEADROOM_PROXY_URL") {
		t.Errorf("error %q does not name the proxy URL setting", err)
	}
}

func TestServeModelFlagIsWired(t *testing.T) {
	// The estimator and tiktoken backends disagree on this string, so an
	// unwired --model shows up as two identical token counts.
	const content = "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"

	count := func(model string) int {
		t.Helper()
		t.Setenv("HEADROOM_CCR_BACKEND", "memory")
		stdout, err := runServe(t, initializeThenCompress(content), "--model", model)
		if err != nil {
			t.Fatalf("mcp serve --model %s: %v", model, err)
		}
		return originalTokensFrom(t, stdout)
	}

	if a, b := count("claude"), count("gpt-4o"); a == b {
		t.Errorf("--model did not reach the tokenizer: both models counted %d tokens", a)
	}
}

// initializeThenCompress builds the stdin script for one headroom_compress call.
func initializeThenCompress(content string) string {
	args, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		panic(err)
	}
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"headroom_compress","arguments":` + string(args) + `}}` + "\n"
}

// originalTokensFrom digs original_tokens out of the tools/call reply.
func originalTokensFrom(t *testing.T, stdout string) int {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil || resp.ID != 2 {
			continue
		}
		if len(resp.Result.Content) == 0 {
			t.Fatalf("tools/call reply carried no content: %s", line)
		}
		var payload struct {
			OriginalTokens int `json:"original_tokens"`
		}
		if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &payload); err != nil {
			t.Fatalf("compress payload is not JSON: %v (%q)", err, resp.Result.Content[0].Text)
		}
		return payload.OriginalTokens
	}
	t.Fatalf("no tools/call reply on stdout: %q", stdout)
	return 0
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
