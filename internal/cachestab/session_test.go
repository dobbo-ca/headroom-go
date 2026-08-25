package cachestab

import (
	"net/http"
	"strings"
	"testing"
)

func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

const convA = `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"alpha"}]}`
const convB = `{"model":"claude-sonnet-5","messages":[{"role":"user","content":"beta"}]}`

// An explicit session id outranks everything: the client declared its session.
func TestSessionKeyPrefersDeclaredIdentity(t *testing.T) {
	full := hdr(
		"X-Headroom-Session-Id", "declared",
		"X-Claude-Code-Session-Id", "cc",
		"Authorization", "Bearer sk-ant-oat01-secret",
		"X-Api-Key", "sk-ant-api-secret",
		"User-Agent", "claude-cli/2.1.234",
	)
	got := SessionKey(full, "127.0.0.1:5000", []byte(convA))
	if !strings.HasPrefix(got, "session:") {
		t.Errorf("key = %q, want the x-headroom-session-id arm", got)
	}
	// Two different conversations under one declared id stay one session:
	// the client said so.
	if other := SessionKey(full, "127.0.0.1:5000", []byte(convB)); other != got {
		t.Error("a declared session id must not vary with the conversation")
	}
}

// Claude Code sends its own session id. Upstream has no such arm; this is a
// deliberate divergence, so pin it.
func TestSessionKeyUsesClaudeCodeSessionId(t *testing.T) {
	h := hdr(
		"X-Claude-Code-Session-Id", "6ec7baaf-ce30-4064-8b7f-fa31d6d7683e",
		"Authorization", "Bearer sk-ant-oat01-secret",
	)
	got := SessionKey(h, "127.0.0.1:5000", []byte(convA))
	if !strings.HasPrefix(got, "claude:") {
		t.Errorf("key = %q, want the claude-code arm to outrank Authorization", got)
	}
	if strings.Contains(got, "6ec7baaf") {
		t.Error("the raw Claude Code session id appears in the key")
	}
}

// Interactive clients send one credential for every conversation they have
// open. Without a conversation fingerprint the two would alternate over one
// slot and report drift on every switch.
func TestSessionKeySeparatesConversationsUnderOneCredential(t *testing.T) {
	h := hdr("Authorization", "Bearer sk-ant-oat01-secret")
	a := SessionKey(h, "127.0.0.1:5000", []byte(convA))
	b := SessionKey(h, "127.0.0.1:5000", []byte(convB))
	if a == b {
		t.Error("two conversations under one credential collapsed to one session key")
	}
	if a != SessionKey(h, "127.0.0.1:5000", []byte(convA)) {
		t.Error("the same conversation produced two different keys")
	}
}

// A prompt cache is per-model, so a small-model sidecar reusing a
// conversation's opener must not share its drift baseline.
func TestSessionKeyFoldsInTheModel(t *testing.T) {
	h := hdr("Authorization", "Bearer tok")
	sonnet := SessionKey(h, "1.2.3.4:1", []byte(convA))
	haiku := SessionKey(h, "1.2.3.4:1",
		[]byte(`{"model":"claude-3-5-haiku-20241022","messages":[{"role":"user","content":"alpha"}]}`))
	if sonnet == haiku {
		t.Error("two models sharing an opener collapsed to one session key")
	}
}

// The breakpoint moves every turn. If it rotated the conversation identity,
// every turn would look like a brand new session and drift would never be seen.
func TestSessionKeySurvivesARelocatedCacheBreakpoint(t *testing.T) {
	h := hdr("Authorization", "Bearer tok")
	plain := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`
	marked := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a",` +
		`"cache_control":{"type":"ephemeral"}}]}]}`
	if SessionKey(h, "1.2.3.4:1", []byte(plain)) != SessionKey(h, "1.2.3.4:1", []byte(marked)) {
		t.Error("a cache_control marker rotated the session identity")
	}
}

// No credential may survive into a value that gets logged or stored.
func TestSessionKeyNeverCarriesARawCredential(t *testing.T) {
	secrets := []string{"sk-ant-oat01-supersecret", "sk-ant-api-supersecret", "claude-cli/2.1.234"}
	headers := []http.Header{
		hdr("Authorization", "Bearer "+secrets[0]),
		hdr("X-Api-Key", secrets[1]),
		hdr("User-Agent", secrets[2]),
	}
	for i, h := range headers {
		key := SessionKey(h, "10.0.0.9:4242", []byte(convA))
		for _, s := range secrets {
			if strings.Contains(key, s) {
				t.Errorf("header set %d leaked %q into the session key %q", i, s, key)
			}
		}
		if strings.Contains(key, "10.0.0.9") {
			t.Errorf("header set %d leaked the client address into %q", i, key)
		}
		if obs := (Observation{SessionDigest: shortDigest(key)}); strings.Contains(obs.SessionDigest, "sk-") {
			t.Error("the logged digest carries a credential")
		}
	}
}

// Falling all the way through must still produce a usable, stable key.
func TestSessionKeyFallsBackToAddressAndUserAgent(t *testing.T) {
	h := hdr("User-Agent", "some-client/1.0")
	a := SessionKey(h, "10.0.0.1:1", []byte(convA))
	b := SessionKey(h, "10.0.0.2:1", []byte(convA))
	if a == b {
		t.Error("two client addresses collapsed to one session key")
	}
	if a != SessionKey(h, "10.0.0.1:1", []byte(convA)) {
		t.Error("the fallback key is not stable")
	}
}

func TestSessionKeyHandlesBodiesWithNoMessages(t *testing.T) {
	h := hdr("Authorization", "Bearer tok")
	for _, body := range []string{`{}`, `{"messages":[]}`, `not json`, ``} {
		if got := SessionKey(h, "1.2.3.4:1", []byte(body)); got == "" {
			t.Errorf("body %q produced an empty session key", body)
		}
	}
}
