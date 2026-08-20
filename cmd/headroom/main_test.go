package main

import (
	"bytes"
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
