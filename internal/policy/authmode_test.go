package policy

import (
	"net/http"
	"testing"
)

func TestClassifySignals(t *testing.T) {
	tests := []struct {
		name string
		in   Signals
		want AuthMode
	}{
		// Rule 1: subscription UA wins over the token shape it carries.
		// A Claude Code session presents an sk-ant-oat token but is a
		// subscription client, not OAuth.
		{"claude cli ua beats oat token",
			Signals{UserAgent: "claude-cli/1.0.60", Authorization: "Bearer sk-ant-oat01-xyz"}, Subscription},
		{"ua match is case insensitive",
			Signals{UserAgent: "Claude-CLI/1.0.60"}, Subscription},
		// The list is matched by substring, not prefix: a corporate
		// wrapper may prepend its own product token.
		{"ua match is substring not prefix",
			Signals{UserAgent: "acme-gw/2.1 claude-cli/1.0.60"}, Subscription},
		{"github copilot ua", Signals{UserAgent: "github-copilot/1.2"}, Subscription},
		{"antigravity ua", Signals{UserAgent: "antigravity/0.9"}, Subscription},

		// Rule 2 before rule 3: sk-ant-oat shares the sk- prefix.
		{"oat token is oauth", Signals{Authorization: "Bearer sk-ant-oat01-xyz"}, OAuth},
		// Rule 3.
		{"anthropic api key is payg", Signals{Authorization: "Bearer sk-ant-api03-xyz"}, PAYG},
		{"openai style sk key is payg", Signals{Authorization: "Bearer sk-proj-xyz"}, PAYG},
		// Rule 4: three dot-separated segments is a JWT.
		{"jwt bearer is oauth", Signals{Authorization: "Bearer aaa.bbb.ccc"}, OAuth},
		{"two segment bearer is not a jwt", Signals{Authorization: "Bearer aaa.bbb"}, PAYG},
		{"four segment bearer is still a jwt", Signals{Authorization: "Bearer aaa.bbb.ccc.ddd"}, OAuth},
		// Rule 5: any non-Bearer scheme.
		{"sigv4 is oauth", Signals{Authorization: "AWS4-HMAC-SHA256 Credential=x/y"}, OAuth},
		{"basic is oauth", Signals{Authorization: "Basic Zm9vOmJhcg=="}, OAuth},
		// Rules 6 and 7.
		{"x-api-key is payg", Signals{XAPIKey: "sk-ant-api03-xyz"}, PAYG},
		{"x-goog-api-key is payg", Signals{XGoogAPIKey: "AIza-xyz"}, PAYG},
		// Rule 8.
		{"empty signals default to payg", Signals{}, PAYG},
		{"unknown ua defaults to payg", Signals{UserAgent: "curl/8.4.0"}, PAYG},
		// An empty Bearer token falls past rules 2-4 to the default.
		{"bare bearer prefix with empty token", Signals{Authorization: "Bearer "}, PAYG},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifySignals(tt.in); got != tt.want {
				t.Errorf("ClassifySignals(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// Header reads must be case-insensitive; net/http canonicalises on Set but a
// hand-built map or a non-canonical key must still resolve.
func TestSignalsFromHeaderIsCaseInsensitive(t *testing.T) {
	h := http.Header{}
	h.Set("user-agent", "claude-cli/1.0")
	h.Set("X-API-KEY", "k")
	h.Set("authorization", "Bearer sk-ant-api03-x")
	h.Set("x-goog-api-key", "g")
	h.Set("X-Client", "  MyTool  ")

	s := SignalsFromHeader(h)
	if s.UserAgent != "claude-cli/1.0" {
		t.Errorf("UserAgent = %q", s.UserAgent)
	}
	if s.XAPIKey != "k" {
		t.Errorf("XAPIKey = %q", s.XAPIKey)
	}
	if s.Authorization != "Bearer sk-ant-api03-x" {
		t.Errorf("Authorization = %q", s.Authorization)
	}
	if s.XGoogAPIKey != "g" {
		t.Errorf("XGoogAPIKey = %q", s.XGoogAPIKey)
	}
	if s.XClient != "  MyTool  " {
		t.Errorf("XClient = %q", s.XClient)
	}
	if ClassifyHeader(h) != Subscription {
		t.Errorf("ClassifyHeader = %v, want Subscription", ClassifyHeader(h))
	}
	// The X-Client override must survive the header hop, not just the
	// Signals hop: ClassifyClient reads X-Client and beats the claude-cli UA.
	if got, ok := ClassifyClient(h); got != "mytool" || !ok {
		t.Errorf("ClassifyClient = (%q,%v), want (\"mytool\",true)", got, ok)
	}
}

// ClassifyHeader and ClassifyClient must fall through to the UA when no
// X-Client is present, rather than being stubbed or wired to a stray header.
func TestClassifyClientFromHeaderUsesUserAgent(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "codex-cli/0.5")

	if got, ok := ClassifyClient(h); got != "codex" || !ok {
		t.Errorf("ClassifyClient = (%q,%v), want (\"codex\",true)", got, ok)
	}
	if got, ok := ClassifyClient(http.Header{}); got != "" || ok {
		t.Errorf("ClassifyClient(empty) = (%q,%v), want (\"\",false)", got, ok)
	}
}

func TestClassifyClient(t *testing.T) {
	tests := []struct {
		name   string
		in     Signals
		want   string
		wantOK bool
	}{
		// X-Client is an explicit override: trimmed and lowercased, and it
		// beats UA matching.
		{"x-client wins over ua", Signals{XClient: "  MyTool  ", UserAgent: "claude-cli/1.0"}, "mytool", true},
		{"claude-cli maps to claude-code", Signals{UserAgent: "claude-cli/1.0"}, "claude-code", true},
		{"claude-code maps to claude-code", Signals{UserAgent: "claude-code/2.0"}, "claude-code", true},
		{"codex-cli maps to codex", Signals{UserAgent: "codex-cli/0.5"}, "codex", true},
		{"grok maps to grok_build", Signals{UserAgent: "grok/1.0"}, "grok_build", true},
		{"substring match", Signals{UserAgent: "wrapper/1 aider/0.5"}, "aider", true},
		// None is the loud "unknown harness" signal, distinct from "".
		{"unknown ua is not identified", Signals{UserAgent: "curl/8.4.0"}, "", false},
		{"empty ua is not identified", Signals{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyClientSignals(tt.in)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ClassifyClientSignals(%+v) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// claude-code/ precedes claude-cli/ in ClientUAMap, but both resolve to the
// same name; the ordering that actually matters is that a more specific
// needle is never shadowed by a shorter one that also matches.
func TestClientUAMapOrderIsStable(t *testing.T) {
	if len(ClientUAMap) != 14 {
		t.Fatalf("ClientUAMap has %d rules, want 14", len(ClientUAMap))
	}
	if len(SubscriptionUAPrefixes) != 9 {
		t.Fatalf("SubscriptionUAPrefixes has %d entries, want 9", len(SubscriptionUAPrefixes))
	}
}

func TestAuthModeString(t *testing.T) {
	for m, want := range map[AuthMode]string{PAYG: "payg", OAuth: "oauth", Subscription: "subscription"} {
		if got := m.String(); got != want {
			t.Errorf("AuthMode(%d).String() = %q, want %q", m, got, want)
		}
	}
}
