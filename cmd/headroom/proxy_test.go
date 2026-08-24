package main

import (
	"strings"
	"testing"
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
