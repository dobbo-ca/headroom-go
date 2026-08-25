package proxy

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresUpstream(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "")
	_, err := Load(Overrides{})
	if err == nil {
		t.Fatal("expected an error when the upstream is unset")
	}
	if !strings.Contains(err.Error(), "HEADROOM_PROXY_UPSTREAM") {
		t.Errorf("error must name the variable, got %q", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")
	c, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:8787" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.MaxBodyBytes != 32<<20 {
		t.Errorf("MaxBodyBytes = %d", c.MaxBodyBytes)
	}
	if c.RequestTimeout != 600*time.Second {
		t.Errorf("RequestTimeout = %v", c.RequestTimeout)
	}
	if c.DialTimeout != 10*time.Second {
		t.Errorf("DialTimeout = %v", c.DialTimeout)
	}
	if !c.Compress {
		t.Error("Compress must default to true")
	}
}

// A flag value must beat the environment.
func TestLoadFlagBeatsEnv(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://env.example")
	t.Setenv("HEADROOM_PROXY_LISTEN", "127.0.0.1:1111")
	c, err := Load(Overrides{Upstream: "https://flag.example", Listen: "127.0.0.1:2222"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://flag.example" {
		t.Errorf("Upstream = %q, want the flag value", c.Upstream)
	}
	if c.Listen != "127.0.0.1:2222" {
		t.Errorf("Listen = %q, want the flag value", c.Listen)
	}
}

func TestLoadTrimsTrailingSlash(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com///")
	c, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://api.anthropic.com" {
		t.Errorf("Upstream = %q, want no trailing slash", c.Upstream)
	}
}

func TestLoadRejectsBadUpstream(t *testing.T) {
	for _, bad := range []string{"ftp://x.example", "not a url", "https://", "/relative"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("HEADROOM_PROXY_UPSTREAM", bad)
			if _, err := Load(Overrides{}); err == nil {
				t.Errorf("Load accepted upstream %q", bad)
			}
		})
	}
}

func TestCompressOffValues(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")
	for _, v := range []string{"disabled", "off", "false", "0", "no", "OFF", " Disabled "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("HEADROOM_PROXY_COMPRESS", v)
			c, err := Load(Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if c.Compress {
				t.Errorf("HEADROOM_PROXY_COMPRESS=%q must disable compression", v)
			}
		})
	}
	for _, v := range []string{"enabled", "on", "true", "1", "yes", ""} {
		t.Run("on:"+v, func(t *testing.T) {
			t.Setenv("HEADROOM_PROXY_COMPRESS", v)
			c, err := Load(Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if !c.Compress {
				t.Errorf("HEADROOM_PROXY_COMPRESS=%q must leave compression on", v)
			}
		})
	}
}

func TestMaxBodyZeroDisablesCap(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")
	t.Setenv("HEADROOM_PROXY_MAX_BODY_BYTES", "0")
	c, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxBodyBytes != 0 {
		t.Errorf("MaxBodyBytes = %d, want 0 (uncapped)", c.MaxBodyBytes)
	}
}

func TestRejectsNegativeAndUnparseableNumbers(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")
	for _, tc := range []struct{ key, val string }{
		{"HEADROOM_PROXY_MAX_BODY_BYTES", "-1"},
		{"HEADROOM_PROXY_MAX_BODY_BYTES", "abc"},
		{"HEADROOM_PROXY_TIMEOUT_SECONDS", "0"},
		{"HEADROOM_PROXY_TIMEOUT_SECONDS", "-5"},
		{"HEADROOM_PROXY_TIMEOUT_SECONDS", "xyz"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(Overrides{}); err == nil {
				t.Errorf("Load accepted %s=%q", tc.key, tc.val)
			}
		})
	}
}

// Every HEADROOM_PROXY_* variable must actually reach the Config; the other
// tests only ever exercise defaults, rejections, and the flag override.
func TestEnvValuesAreUsed(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")
	t.Setenv("HEADROOM_PROXY_LISTEN", "127.0.0.1:9999")
	t.Setenv("HEADROOM_PROXY_MAX_BODY_BYTES", "1024")
	t.Setenv("HEADROOM_PROXY_TIMEOUT_SECONDS", "5")

	c, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9999" {
		t.Errorf("Listen = %q, want the env value", c.Listen)
	}
	if c.MaxBodyBytes != 1024 {
		t.Errorf("MaxBodyBytes = %d, want the env value 1024", c.MaxBodyBytes)
	}
	if c.RequestTimeout != 5*time.Second {
		t.Errorf("RequestTimeout = %v, want the env value 5s", c.RequestTimeout)
	}
}

// A --upstream flag takes the same validation and normalisation as the
// environment variable; it must not be a way around the scheme check.
func TestFlagUpstreamIsValidatedAndTrimmed(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://env.example")

	for _, bad := range []string{"ftp://x.example", "not a url", "https://", "/relative"} {
		t.Run("rejects:"+bad, func(t *testing.T) {
			if _, err := Load(Overrides{Upstream: bad}); err == nil {
				t.Errorf("Load accepted --upstream %q", bad)
			}
		})
	}

	c, err := Load(Overrides{Upstream: "https://flag.example///"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Upstream != "https://flag.example" {
		t.Errorf("Upstream = %q, want no trailing slash", c.Upstream)
	}
}

// Replay mutates bytes inside the cache-frozen prefix, so it is off unless an
// operator asks for it — and asking must actually work. A flag that parses to
// nothing ships dead, and this repo has done that before.
func TestReplayIsOffByDefaultAndOptedInByEnv(t *testing.T) {
	t.Setenv("HEADROOM_PROXY_UPSTREAM", "https://api.anthropic.com")

	for _, v := range []string{"", "off", "false", "0", "no", "disabled", "maybe"} {
		t.Run("off:"+v, func(t *testing.T) {
			t.Setenv("HEADROOM_PROXY_REPLAY", v)
			c, err := Load(Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if c.Replay {
				t.Errorf("HEADROOM_PROXY_REPLAY=%q must leave replay off", v)
			}
		})
	}
	for _, v := range []string{"enabled", "on", "true", "1", "yes", "ON", " True "} {
		t.Run("on:"+v, func(t *testing.T) {
			t.Setenv("HEADROOM_PROXY_REPLAY", v)
			c, err := Load(Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if !c.Replay {
				t.Errorf("HEADROOM_PROXY_REPLAY=%q must enable replay", v)
			}
		})
	}
}
