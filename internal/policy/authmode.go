// Package policy classifies an inbound request's auth mode from its headers
// and resolves the per-mode compression policy the live-zone dispatcher
// consults. It is pure: no I/O, and header values are never logged.
package policy

import (
	"net/http"
	"strings"
)

// AuthMode is the auth class a request is routed through.
type AuthMode int

const (
	// PAYG is an API-key request (Anthropic, OpenAI, Gemini). The safest
	// default: a misclassified request only costs a re-run.
	PAYG AuthMode = iota
	// OAuth is a token-bearing session (Claude Pro/Max, Codex, Cursor,
	// Copilot) and any non-Bearer scheme such as AWS SigV4.
	OAuth
	// Subscription is a first-party CLI harness identified by User-Agent.
	Subscription
)

func (m AuthMode) String() string {
	switch m {
	case OAuth:
		return "oauth"
	case Subscription:
		return "subscription"
	default:
		return "payg"
	}
}

// SubscriptionUAPrefixes are matched as SUBSTRINGS of the lowercased
// User-Agent, not prefixes, despite the upstream name this list is ported
// from. Some clients sit behind a corporate wrapper that prepends its own
// product token, so an anchored match would miss them.
var SubscriptionUAPrefixes = []string{
	"claude-cli/",
	"claude-code/",
	"codex-cli/",
	"cursor/",
	"grok/",
	"claude-vscode/",
	"github-copilot/",
	"anthropic-cli/",
	"antigravity/",
}

// ClientUARule maps a User-Agent substring to a harness name.
type ClientUARule struct {
	Needle string
	Name   string
}

// ClientUAMap is ordered; the first matching needle wins.
var ClientUAMap = []ClientUARule{
	{"claude-code/", "claude-code"},
	{"claude-cli/", "claude-code"},
	{"claude-vscode/", "claude-vscode"},
	{"anthropic-cli/", "anthropic-cli"},
	{"codex-cli/", "codex"},
	{"cursor/", "cursor"},
	{"grok/", "grok_build"},
	{"zed/", "zed"},
	{"aider/", "aider"},
	{"droid/", "droid"},
	{"opencode/", "opencode"},
	{"github-copilot/", "copilot"},
	{"antigravity/", "antigravity"},
	{"strands-agents/", "strands"},
}

// Signals are the normalized header values the pure classifiers read.
type Signals struct {
	UserAgent     string
	Authorization string
	XAPIKey       string
	XGoogAPIKey   string
	XClient       string
}

// SignalsFromHeader adapts an http.Header. http.Header.Get canonicalises the
// key, so a non-canonical key set via Set resolves; a map literal built by
// hand with a lowercase key does not, which is why callers should use Set.
func SignalsFromHeader(h http.Header) Signals {
	return Signals{
		UserAgent:     h.Get("User-Agent"),
		Authorization: h.Get("Authorization"),
		XAPIKey:       h.Get("X-Api-Key"),
		XGoogAPIKey:   h.Get("X-Goog-Api-Key"),
		XClient:       h.Get("X-Client"),
	}
}

const bearerPrefix = "Bearer "

// ClassifySignals resolves the auth mode. The rule order is load-bearing:
// the subscription UA check runs first because a subscription CLI carries an
// OAuth-shaped token, and the sk-ant-oat check runs before the broader sk-
// check because sk-ant-oat also starts with sk-.
func ClassifySignals(s Signals) AuthMode {
	ua := strings.ToLower(s.UserAgent)
	for _, needle := range SubscriptionUAPrefixes {
		if strings.Contains(ua, needle) {
			return Subscription
		}
	}

	if auth := s.Authorization; strings.HasPrefix(auth, bearerPrefix) {
		token := auth[len(bearerPrefix):]
		switch {
		case strings.HasPrefix(token, "sk-ant-oat"):
			return OAuth
		case strings.HasPrefix(token, "sk-ant-api"), strings.HasPrefix(token, "sk-"):
			return PAYG
		case len(strings.Split(token, ".")) >= 3:
			return OAuth
		}
	} else if auth != "" {
		return OAuth
	}

	if s.XAPIKey != "" {
		return PAYG
	}
	if s.XGoogAPIKey != "" {
		return PAYG
	}
	return PAYG
}

// ClassifyHeader is ClassifySignals over an http.Header.
func ClassifyHeader(h http.Header) AuthMode { return ClassifySignals(SignalsFromHeader(h)) }

// ClassifyClientSignals identifies the client harness. ok is false when no
// signal identifies one — a loud "unknown harness" distinct from an empty
// name, so downstream can group unidentified clients rather than silently
// bucketing them.
func ClassifyClientSignals(s Signals) (string, bool) {
	if explicit := strings.ToLower(strings.TrimSpace(s.XClient)); explicit != "" {
		return explicit, true
	}
	ua := strings.ToLower(s.UserAgent)
	if ua == "" {
		return "", false
	}
	for _, rule := range ClientUAMap {
		if strings.Contains(ua, rule.Needle) {
			return rule.Name, true
		}
	}
	return "", false
}

// ClassifyClient is ClassifyClientSignals over an http.Header.
func ClassifyClient(h http.Header) (string, bool) { return ClassifyClientSignals(SignalsFromHeader(h)) }
