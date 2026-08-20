// Package config resolves headroom's runtime settings from command-line flags,
// HEADROOM_* environment variables, and built-in defaults, in that order.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/paths"
)

// Defaults. The 3600s CCR lifetime is the spec's fixed value for the MCP path.
const (
	defaultBackend  = "sqlite"
	defaultTTL      = 3600 * time.Second
	defaultProxyURL = "http://127.0.0.1:8787"
	defaultModel    = "claude"
)

// Config is the resolved runtime configuration.
type Config struct {
	CCRBackend  string
	CCRPath     string // SQLite file path; empty when CCRBackend is "memory"
	CCRTTL      time.Duration
	CCRCapacity int
	ProxyURL    string
	Model       string
}

// Overrides carries command-line values. A nil field means the flag was not
// set, so the environment variable or the default wins for that field.
type Overrides struct {
	CCRBackend *string
	CCRPath    *string
	ProxyURL   *string
	Model      *string
}

// FromEnv resolves configuration from the environment and defaults only.
func FromEnv() (Config, error) { return Load(Overrides{}) }

// Load resolves configuration with flag > env > default precedence and
// validates every field. An exported-but-empty variable counts as unset.
func Load(ov Overrides) (Config, error) {
	var c Config

	c.CCRBackend = pick(ov.CCRBackend, "HEADROOM_CCR_BACKEND", defaultBackend)
	switch c.CCRBackend {
	case "sqlite", "memory":
	default:
		return Config{}, fmt.Errorf("config: HEADROOM_CCR_BACKEND must be \"sqlite\" or \"memory\", got %q", c.CCRBackend)
	}

	if c.CCRBackend == "sqlite" {
		p := pick(ov.CCRPath, "HEADROOM_CCR_PATH", "")
		if p == "" {
			var err error
			if p, err = paths.CCRDBPath(); err != nil {
				return Config{}, fmt.Errorf("config: resolve default CCR path: %w", err)
			}
		}
		c.CCRPath = p
	}

	ttl, err := positiveInt("HEADROOM_CCR_TTL_SECONDS", int(defaultTTL/time.Second))
	if err != nil {
		return Config{}, err
	}
	c.CCRTTL = time.Duration(ttl) * time.Second

	if c.CCRCapacity, err = positiveInt("HEADROOM_CCR_CAPACITY", ccr.DefaultCapacity); err != nil {
		return Config{}, err
	}

	c.ProxyURL = pick(ov.ProxyURL, "HEADROOM_PROXY_URL", defaultProxyURL)
	u, err := url.Parse(c.ProxyURL)
	if err != nil {
		return Config{}, fmt.Errorf("config: HEADROOM_PROXY_URL %q is not a URL: %w", c.ProxyURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Config{}, fmt.Errorf("config: HEADROOM_PROXY_URL %q needs an http or https scheme", c.ProxyURL)
	}
	if u.Host == "" {
		return Config{}, fmt.Errorf("config: HEADROOM_PROXY_URL %q has no host", c.ProxyURL)
	}

	c.Model = pick(ov.Model, "HEADROOM_MODEL", defaultModel)
	return c, nil
}

// BackendConfig converts the resolved settings into a ccr.BackendConfig.
func (c Config) BackendConfig() ccr.BackendConfig {
	kind := ccr.SQLite
	if c.CCRBackend == "memory" {
		kind = ccr.InMemory
	}
	return ccr.BackendConfig{
		Kind:       kind,
		Capacity:   c.CCRCapacity,
		TTLSeconds: uint64(c.CCRTTL / time.Second),
		Path:       c.CCRPath,
	}
}

func pick(flagVal *string, envKey, def string) string {
	if flagVal != nil && *flagVal != "" {
		return *flagVal
	}
	if v := osGetenv(envKey); v != "" {
		return v
	}
	return def
}

func positiveInt(envKey string, def int) (int, error) {
	v := osGetenv(envKey)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", envKey, v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("config: %s must be greater than 0, got %d", envKey, n)
	}
	return n, nil
}

var osGetenv = os.Getenv
