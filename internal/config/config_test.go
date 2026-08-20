package config

import (
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HEADROOM_HOME", "HEADROOM_CCR_BACKEND", "HEADROOM_CCR_PATH",
		"HEADROOM_CCR_TTL_SECONDS", "HEADROOM_CCR_CAPACITY",
		"HEADROOM_PROXY_URL", "HEADROOM_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("HEADROOM_HOME", t.TempDir())

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if c.CCRBackend != "sqlite" {
		t.Errorf("CCRBackend = %q, want sqlite", c.CCRBackend)
	}
	if !strings.HasSuffix(c.CCRPath, "ccr.db") {
		t.Errorf("CCRPath = %q, want a path ending in ccr.db", c.CCRPath)
	}
	if c.CCRTTL != time.Hour {
		t.Errorf("CCRTTL = %v, want 1h", c.CCRTTL)
	}
	if c.CCRCapacity != ccr.DefaultCapacity {
		t.Errorf("CCRCapacity = %d, want %d", c.CCRCapacity, ccr.DefaultCapacity)
	}
	if c.ProxyURL != "http://127.0.0.1:8787" {
		t.Errorf("ProxyURL = %q, want http://127.0.0.1:8787", c.ProxyURL)
	}
	if c.Model != "claude" {
		t.Errorf("Model = %q, want claude", c.Model)
	}
}

func TestEnvOverridesDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("HEADROOM_CCR_BACKEND", "memory")
	t.Setenv("HEADROOM_CCR_TTL_SECONDS", "60")
	t.Setenv("HEADROOM_CCR_CAPACITY", "7")
	t.Setenv("HEADROOM_PROXY_URL", "https://proxy.example:9999")
	t.Setenv("HEADROOM_MODEL", "gpt-4o")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if c.CCRBackend != "memory" {
		t.Errorf("CCRBackend = %q, want memory", c.CCRBackend)
	}
	if c.CCRPath != "" {
		t.Errorf("CCRPath = %q, want empty for the memory backend", c.CCRPath)
	}
	if c.CCRTTL != 60*time.Second {
		t.Errorf("CCRTTL = %v, want 60s", c.CCRTTL)
	}
	if c.CCRCapacity != 7 {
		t.Errorf("CCRCapacity = %d, want 7", c.CCRCapacity)
	}
	if c.ProxyURL != "https://proxy.example:9999" {
		t.Errorf("ProxyURL = %q", c.ProxyURL)
	}
	if c.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", c.Model)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("HEADROOM_CCR_BACKEND", "sqlite")
	t.Setenv("HEADROOM_HOME", t.TempDir())
	t.Setenv("HEADROOM_PROXY_URL", "http://from-env:1111")

	backend := "memory"
	proxy := "http://from-flag:2222"
	c, err := Load(Overrides{CCRBackend: &backend, ProxyURL: &proxy})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c.CCRBackend != "memory" {
		t.Errorf("CCRBackend = %q, want memory (flag wins)", c.CCRBackend)
	}
	if c.ProxyURL != "http://from-flag:2222" {
		t.Errorf("ProxyURL = %q, want the flag value", c.ProxyURL)
	}
}

func TestEmptyEnvVarFallsBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("HEADROOM_HOME", t.TempDir())
	t.Setenv("HEADROOM_CCR_BACKEND", "")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if c.CCRBackend != "sqlite" {
		t.Errorf("CCRBackend = %q, want the sqlite default", c.CCRBackend)
	}
}

func TestRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"unknown backend", "HEADROOM_CCR_BACKEND", "redis"},
		{"non-numeric ttl", "HEADROOM_CCR_TTL_SECONDS", "soon"},
		{"zero ttl", "HEADROOM_CCR_TTL_SECONDS", "0"},
		{"negative ttl", "HEADROOM_CCR_TTL_SECONDS", "-1"},
		{"non-numeric capacity", "HEADROOM_CCR_CAPACITY", "lots"},
		{"zero capacity", "HEADROOM_CCR_CAPACITY", "0"},
		{"proxy url without scheme", "HEADROOM_PROXY_URL", "127.0.0.1:8787"},
		{"proxy url with wrong scheme", "HEADROOM_PROXY_URL", "ftp://host/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("HEADROOM_HOME", t.TempDir())
			t.Setenv(tc.key, tc.val)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("FromEnv() with %s=%q returned no error", tc.key, tc.val)
			}
		})
	}
}

func TestBackendConfigMapsToCCR(t *testing.T) {
	sq := Config{CCRBackend: "sqlite", CCRPath: "/tmp/x.db", CCRTTL: 90 * time.Second, CCRCapacity: 5}
	got := sq.BackendConfig()
	if got.Kind != ccr.SQLite {
		t.Errorf("Kind = %v, want ccr.SQLite", got.Kind)
	}
	if got.Path != "/tmp/x.db" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.TTLSeconds != 90 {
		t.Errorf("TTLSeconds = %d, want 90", got.TTLSeconds)
	}

	mem := Config{CCRBackend: "memory", CCRTTL: time.Minute, CCRCapacity: 5}
	if got := mem.BackendConfig(); got.Kind != ccr.InMemory || got.Capacity != 5 {
		t.Errorf("memory BackendConfig() = %+v, want InMemory with capacity 5", got)
	}
}
