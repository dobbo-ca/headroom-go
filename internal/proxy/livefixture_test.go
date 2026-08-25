package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/cachecontrol"
	"github.com/dobbo-ca/headroom-go/internal/livezone"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/tidwall/gjson"
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

// What headroom actually does to a real Claude Code request.
//
// The fixture is a genuine `claude -p` request body captured through
// `headroom proxy`, with every string value replaced by filler of the same
// length. Structure, block counts, cache_control placement, tool count and
// byte sizes are the real ones; no private content survives.
//
// The result is uncomfortable and must not regress silently: headroom
// compresses NOTHING. This test pins that, so the day a change makes the
// proxy earn its keep on real traffic, this test fails and someone updates
// it deliberately.
func TestLiveFixtureClaudeCodeRequestIsNotCompressed(t *testing.T) {
	body := readFixture(t, "claude_code_request_redacted.json")

	var upstreamBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	defer up.Close()

	store := newMapStore()
	srv := New(Deps{
		Config: Config{Upstream: up.URL, MaxBodyBytes: 32 << 20, Compress: true,
			RequestTimeout: 30 * time.Second, DialTimeout: 10 * time.Second},
		Store: store, Router: router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"), Version: "fixture",
	})
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if len(upstreamBody) != len(body) {
		t.Fatalf("upstream got %d bytes, client sent %d -- headroom now compresses real "+
			"Claude Code traffic. That is the goal: update this test deliberately",
			len(upstreamBody), len(body))
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries for an uncompressed request", store.Len())
	}

	// Reason one: Claude Code cache-pins the LAST message, so the frozen
	// floor covers every message and there is no live zone at all.
	frozen, _ := cachecontrol.ComputeFrozenCount(body)
	res := livezone.Dispatch(body, livezone.Options{
		Router: router.NewDefault(), Store: newMapStore(),
		Tokenizer: tokenizer.GetTokenizer(livezone.DefaultModel), FrozenCount: -1})
	if res.Reason != livezone.ReasonNoLiveZone {
		t.Errorf("Reason = %q, want %q (frozen floor %d over %d messages)",
			res.Reason, livezone.ReasonNoLiveZone, frozen,
			gjson.GetBytes(body, "messages.#").Int())
	}

	// Reason two, independent of the first: even with the floor forced to 0,
	// nothing in the live zone is compressible. The big block is
	// conversational prose, which no heuristic compressor recognises; the
	// other is below the byte threshold.
	unfrozen := livezone.Dispatch(body, livezone.Options{
		Router: router.NewDefault(), Store: newMapStore(),
		Tokenizer: tokenizer.GetTokenizer(livezone.DefaultModel), FrozenCount: 0})
	if unfrozen.Applied {
		t.Errorf("with the frozen floor ignored the body compressed after all (reason %q); "+
			"the cache_control floor is then the only thing suppressing savings", unfrozen.Reason)
	}
	if unfrozen.Reason != livezone.ReasonNoCandidates {
		t.Errorf("unfrozen Reason = %q, want %q", unfrozen.Reason, livezone.ReasonNoCandidates)
	}

	// Reason three, structural: three quarters of the request is `tools`, and
	// the dispatcher only ever walks messages[*].content[*].
	total := int64(len(body))
	tools := int64(len(gjson.GetBytes(body, "tools").Raw))
	if pct := 100 * tools / total; pct < 60 {
		t.Errorf("tools are %d%% of the body; the fixture no longer shows why "+
			"a messages-only dispatcher cannot help here", pct)
	}
	t.Logf("real shape: %d bytes total, tools %d%%, dispatcher can reach messages only",
		total, 100*tools/total)
}
