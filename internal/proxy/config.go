// Package proxy is headroom's HTTP front door. It forwards requests to an
// upstream LLM API, compressing REQUEST bodies through the live-zone
// dispatcher on the way. Responses stream back verbatim and are never
// compressed: rewriting a response corrupts live token rendering.
package proxy

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListen         = "0.0.0.0:8787"
	defaultMaxBodyBytes   = 32 << 20
	defaultTimeoutSeconds = 600
	defaultDialTimeout    = 10 * time.Second
)

// Config is the resolved proxy configuration.
type Config struct {
	Upstream       string
	Listen         string
	MaxBodyBytes   int64 // 0 means uncapped
	RequestTimeout time.Duration
	DialTimeout    time.Duration
	Compress       bool
	// Replay re-sends a previously compressed block in its compressed form
	// on every later turn of the same session, so the provider's cached
	// prefix keeps matching. It is what makes compressing anything the
	// client re-sends worth doing at all.
	//
	// ON by default since the guard rails below could be demonstrated.
	// Set HEADROOM_PROXY_REPLAY=off to disable.
	//
	// It was off because turning it on without an MCP server left the model
	// looking at <<ccr:HASH>> markers it could not dereference, for a whole
	// session rather than one turn. `headroom wrap` now brings that server
	// up on the same store and REFUSES to start a session it cannot wire,
	// so the failure mode the opt-in guarded against no longer reaches a
	// user by accident.
	//
	// Three things hold it up, and each has a test that fails without it:
	//
	//  1. A client that declares no session id gets no replay at all. The
	//     inferred identities are per-tenant, not per-conversation.
	//  2. A marker whose original the store cannot hand back never goes on
	//     the wire, in either marker surface.
	//  3. Entries are swept once the client stops re-sending their block,
	//     so a proxy running for days holds the working set, not the day.
	Replay bool
	// ImageFit downsamples oversized images to the standard vision tier
	// (1568px long edge, 1568 visual tokens). OFF by default: computer-use
	// and screenshot-understanding are exactly the workloads that need
	// fidelity. Measure before enabling.
	ImageFit bool
}

// Overrides carries command-line values; empty means unset.
type Overrides struct {
	Upstream string
	Listen   string
}

// offValues disable a boolean setting. Same vocabulary the rest of the
// project uses, so operators do not need a second one.
var offValues = map[string]bool{"disabled": true, "off": true, "false": true, "0": true, "no": true}

// onValues enable a boolean setting that defaults OFF. Mirrors offValues so
// operators do not need a third vocabulary.
var onValues = map[string]bool{"enabled": true, "on": true, "true": true, "1": true, "yes": true}

// Load resolves the proxy configuration with flag > env > default precedence.
func Load(ov Overrides) (Config, error) {
	var c Config

	up := pick(ov.Upstream, "HEADROOM_PROXY_UPSTREAM", "")
	if up == "" {
		return Config{}, fmt.Errorf("proxy: HEADROOM_PROXY_UPSTREAM is required (or pass --upstream)")
	}
	up = strings.TrimRight(up, "/")
	u, err := url.Parse(up)
	if err != nil {
		return Config{}, fmt.Errorf("proxy: HEADROOM_PROXY_UPSTREAM %q is not a URL: %w", up, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Config{}, fmt.Errorf("proxy: HEADROOM_PROXY_UPSTREAM %q needs an http or https scheme", up)
	}
	if u.Host == "" {
		return Config{}, fmt.Errorf("proxy: HEADROOM_PROXY_UPSTREAM %q has no host", up)
	}
	c.Upstream = up

	c.Listen = pick(ov.Listen, "HEADROOM_PROXY_LISTEN", defaultListen)

	if c.MaxBodyBytes, err = nonNegativeInt64("HEADROOM_PROXY_MAX_BODY_BYTES", defaultMaxBodyBytes); err != nil {
		return Config{}, err
	}

	secs, err := positiveInt("HEADROOM_PROXY_TIMEOUT_SECONDS", defaultTimeoutSeconds)
	if err != nil {
		return Config{}, err
	}
	c.RequestTimeout = time.Duration(secs) * time.Second
	c.DialTimeout = defaultDialTimeout

	c.Compress = !offValues[strings.ToLower(strings.TrimSpace(os.Getenv("HEADROOM_PROXY_COMPRESS")))]
	c.Replay = !offValues[strings.ToLower(strings.TrimSpace(os.Getenv("HEADROOM_PROXY_REPLAY")))]
	c.ImageFit = onValues[strings.ToLower(strings.TrimSpace(os.Getenv("HEADROOM_PROXY_IMAGE_FIT")))]
	return c, nil
}

func pick(flagVal, envKey, def string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

func nonNegativeInt64(envKey string, def int64) (int64, error) {
	v := os.Getenv(envKey)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("proxy: %s must be an integer, got %q", envKey, v)
	}
	if n < 0 {
		return 0, fmt.Errorf("proxy: %s must not be negative, got %d", envKey, n)
	}
	return n, nil
}

func positiveInt(envKey string, def int) (int, error) {
	v := os.Getenv(envKey)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("proxy: %s must be an integer, got %q", envKey, v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("proxy: %s must be greater than 0, got %d", envKey, n)
	}
	return n, nil
}
