package cachestab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"
)

// SessionKey derives a stable per-conversation identity. The returned string
// is opaque and must never be logged; pass it to DriftState.Observe, which
// logs only a short digest.
//
// Priority, most trustworthy first:
//
//  1. x-headroom-session-id — the client declared its session, so believe it.
//  2. x-claude-code-session-id — Claude Code sends this on every request.
//     Upstream has no such arm; it is added here because Claude Code is this
//     proxy's primary client and a client-declared conversation id beats any
//     identity we can infer.
//  3. Authorization, then x-api-key, then (client address, User-Agent).
//
// Arms 3 and below identify a TENANT, not a conversation: an interactive
// client sends the same credential for every conversation it has open. Those
// arms therefore fold in a fingerprint of the conversation's first message, so
// two conversations under one credential do not alternate over one slot and
// report drift on every switch.
//
// Every credential is hashed before it is returned. The raw value never leaves
// this function.
func SessionKey(h http.Header, remoteAddr string, body []byte) string {
	if sid := h.Get("X-Headroom-Session-Id"); sid != "" {
		return "session:" + shortDigest(sid)
	}
	if sid := h.Get("X-Claude-Code-Session-Id"); sid != "" {
		return "claude:" + shortDigest(sid)
	}

	conv := conversationDiscriminator(body)
	if auth := h.Get("Authorization"); auth != "" {
		return "auth:" + shortDigest(auth) + ":" + conv
	}
	if key := h.Get("X-Api-Key"); key != "" {
		return "apikey:" + shortDigest(key) + ":" + conv
	}
	// Last resort. The address and User-Agent are hashed together so a full
	// UA string cannot leak through a caller that forgets the log contract.
	return "addr:" + shortDigest(remoteAddr+"\x00"+h.Get("User-Agent")) + ":" + conv
}

// conversationDiscriminator fingerprints (model, first message) in 16 hex
// characters, or "-" when the body carries no messages.
//
// The model is folded in because prompt caches are per-model: a small-model
// sidecar reusing a conversation's opener must not share that conversation's
// drift baseline.
//
// The system prompt and tools are deliberately excluded. They are the axes
// being measured, and agentic clients legitimately mutate them mid-session. An
// identity built from them would rotate at exactly the moment the detector
// should be reporting drift.
//
// Known trade-off, inherited from upstream: a client that REWRITES its first
// message — history compaction, a rolling window — re-keys to a fresh session,
// so the rewrite shows up as a first request rather than as drift. Sending an
// explicit session id pins the identity and reports it correctly.
func conversationDiscriminator(body []byte) string {
	first := gjson.GetBytes(body, "messages.0")
	if !first.Exists() {
		return "-"
	}
	var v any
	if err := json.Unmarshal([]byte(first.Raw), &v); err != nil {
		return "-"
	}
	canonical, err := json.Marshal(stripCacheControl(v, false))
	if err != nil {
		return "-"
	}
	sum := sha256.New()
	sum.Write([]byte(gjson.GetBytes(body, "model").String()))
	// A NUL separator domain-separates the model from the message bytes, so
	// no (model, message) pair can alias another by shifting the boundary.
	sum.Write([]byte{0})
	sum.Write(canonical)
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// DeclaredSessionKey returns a session key ONLY when the client declared its
// own conversation id, and reports false otherwise.
//
// [SessionKey] always returns something, because drift detection is
// observation and a wrong guess there costs a log line. Replay is different:
// it puts bytes on the wire. Its identity has to be one the CLIENT owns.
//
// The inferred arms — credential, or client address plus User-Agent, each
// folded with a fingerprint of the first message — identify a TENANT, not a
// conversation, and the address arm rotates per TCP connection. Replaying
// against an identity like that is a guess: at best it silently does nothing,
// and at worst two conversations share a slot and one is served the other's
// compressed blocks. Returning false makes the no-op explicit and auditable
// instead of accidental.
func DeclaredSessionKey(h http.Header) (string, bool) {
	if sid := h.Get("X-Headroom-Session-Id"); sid != "" {
		return "session:" + shortDigest(sid), true
	}
	if sid := h.Get("X-Claude-Code-Session-Id"); sid != "" {
		return "claude:" + shortDigest(sid), true
	}
	return "", false
}
