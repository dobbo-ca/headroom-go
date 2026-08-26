package cachestab

import (
	"net/http"
	"testing"
)

// The join `headroom perf` performs is: transcript filename -> session digest
// -> ledger line. It only works if the digest a proxy records for a live
// Claude Code request equals the one computed from the transcript's name.
//
// The session id below is the one Claude Code 2.1.234 actually sent while
// writing fe814859-...jsonl, and a5bc75b9872885ce is what the proxy logged
// for that session. A mismatch here means `headroom perf` silently reports
// zero headroom turns against a real day of use.
func TestClaudeSessionDigestMatchesWhatTheProxyRecords(t *testing.T) {
	const sessionID = "fe814859-5860-4fa3-a2dc-7906e146c71a"
	const observed = "a5bc75b9872885ce"

	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", sessionID)
	live := Digest(SessionKey(h, "127.0.0.1:1234", []byte(`{"messages":[]}`)))

	if live != observed {
		t.Errorf("a live request digests to %q, want the captured %q", live, observed)
	}
	if got := ClaudeSessionDigest(sessionID); got != live {
		t.Errorf("ClaudeSessionDigest = %q, want the live digest %q", got, live)
	}
}

// A different session must not collide, or the report would attribute one
// session's turns to another.
func TestClaudeSessionDigestDistinguishesSessions(t *testing.T) {
	a := ClaudeSessionDigest("fe814859-5860-4fa3-a2dc-7906e146c71a")
	b := ClaudeSessionDigest("0a3af18c-eec6-4878-878b-1043d8e78a07")
	if a == b {
		t.Fatal("two session ids digest to the same value")
	}
	if b != "3b75b10c2b1deda1" {
		t.Errorf("digest = %q, want the captured 3b75b10c2b1deda1", b)
	}
}
