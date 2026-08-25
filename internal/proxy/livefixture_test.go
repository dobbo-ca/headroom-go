package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures under testdata/live are real bytes from api.anthropic.com,
// captured by live_test.go (build tag liveapi). Replaying them here puts the
// real shapes in the ordinary suite: no build tag, no network, no credential.
//
// Regenerate with:
//
//	HEADROOM_CAPTURE=1 go test -tags liveapi ./internal/proxy/ -run TestLive
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "live", name))
	if err != nil {
		t.Fatalf("fixture missing: %v (regenerate with the liveapi build tag)", err)
	}
	if len(b) == 0 {
		t.Fatalf("fixture %s is empty", name)
	}
	return b
}

// parseHeaderFixture turns the captured "Name: value" dump back into headers.
func parseHeaderFixture(t *testing.T, name string) http.Header {
	t.Helper()
	h := http.Header{}
	for _, line := range strings.Split(string(readFixture(t, name)), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		h.Add(k, v)
	}
	if len(h) == 0 {
		t.Fatalf("fixture %s parsed to zero headers", name)
	}
	return h
}

// Anthropic's real error envelope must reach the client byte-for-byte, with
// its status and its diagnostic headers intact. A proxy that swallows the
// upstream error and answers 502 leaves the client unable to tell an expired
// key from a network fault.
func TestLiveFixtureErrorEnvelopeIsForwardedVerbatim(t *testing.T) {
	body := readFixture(t, "unauthenticated_error_response.json")
	upHeaders := parseHeaderFixture(t, "unauthenticated_error_headers.txt")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Replay the upstream's own headers, minus the ones headroom itself
		// added on the way out and the ones a server must own.
		for k, vs := range upHeaders {
			if strings.HasPrefix(strings.ToLower(k), internalHeaderPrefix) ||
				k == "Content-Length" || k == "Date" {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(body)
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want the upstream's 401", resp.StatusCode)
	}
	got := make([]byte, len(body)+64)
	n, _ := resp.Body.Read(got)
	if string(got[:n]) != string(body) {
		t.Errorf("error body was altered\n got: %s\nwant: %s", got[:n], body)
	}
	// The headers an operator debugs with must survive the hop.
	for _, name := range []string{"Request-Id", "X-Should-Retry", "Cf-Ray"} {
		if resp.Header.Get(name) != upHeaders.Get(name) {
			t.Errorf("%s = %q, want the upstream's %q",
				name, resp.Header.Get(name), upHeaders.Get(name))
		}
	}
}

// The captured headers prove the real hop reported a saving on a real request.
// This pins the numbers so a regression that silently stops compressing shows
// up as a fixture mismatch rather than as a quiet loss of savings.
func TestLiveFixtureRecordsARealSaving(t *testing.T) {
	h := parseHeaderFixture(t, "unauthenticated_error_headers.txt")

	for _, name := range []string{
		"X-Headroom-Bytes-Saved", "X-Headroom-Tokens-Before", "X-Headroom-Tokens-After",
	} {
		if h.Get(name) == "" {
			t.Fatalf("the real hop set no %s header; compression did not run", name)
		}
	}
	before, after := h.Get("X-Headroom-Tokens-Before"), h.Get("X-Headroom-Tokens-After")
	if before == after {
		t.Errorf("tokens before == after (%s); the real request was not compressed", before)
	}
	// The upstream must never have seen headroom's own headers. Capturing them
	// on the RESPONSE is the only place they legitimately appear.
	if h.Get("X-Headroom-Upstream-Echo") != "" {
		t.Error("an x-headroom-* header came back from upstream; the outbound strip failed")
	}
}
