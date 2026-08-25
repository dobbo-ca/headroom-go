//go:build liveapi

// Live contact with api.anthropic.com. Guarded by a build tag rather than
// t.Skip so it can never report a silent pass: the suite either compiles this
// file and talks to the real API, or does not include it at all.
//
//	ANTHROPIC_API_KEY=sk-ant-api... go test -tags liveapi ./internal/proxy/ -run TestLive -v
//
// Set HEADROOM_CAPTURE=1 to rewrite testdata/live/*. The captured bytes feed
// livefixture_test.go, which runs in the ordinary suite.
//
// The key MUST be a pay-as-you-go API key. A subscription OAuth token is not
// a valid credential for this endpoint and would also classify differently.
package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

const (
	liveUpstream = "https://api.anthropic.com"
	liveModel    = "claude-3-5-haiku-20241022"
	liveVersion  = "2023-06-01"
	// liveBeta exercises the anthropic-beta header end to end. Two values so
	// the comma-joined form is covered, not just the single-token one.
	liveBeta = "token-efficient-tools-2025-02-19,prompt-caching-2024-07-31"
)

// liveKey returns the PAYG key or fails. Failing beats skipping: a skipped
// live test looks identical to a passing one in CI output.
func liveKey(t *testing.T) string {
	t.Helper()
	k := os.Getenv("ANTHROPIC_API_KEY")
	if k == "" {
		t.Fatal("ANTHROPIC_API_KEY is not set; the liveapi tag requires a real PAYG key")
	}
	if !strings.HasPrefix(k, "sk-ant-api") {
		t.Fatalf("ANTHROPIC_API_KEY does not look like a PAYG API key (want sk-ant-api… prefix); "+
			"got a %d-character value starting %q", len(k), k[:min(6, len(k))])
	}
	return k
}

// liveFront starts the real proxy in front of api.anthropic.com and returns
// its base URL plus the CCR store the dispatcher writes to.
func liveFront(t *testing.T) (string, *mapStore) {
	t.Helper()
	store := newMapStore()
	srv := New(Deps{
		Config: Config{
			Upstream:       liveUpstream,
			MaxBodyBytes:   32 << 20,
			Compress:       true,
			RequestTimeout: 120 * time.Second,
			DialTimeout:    10 * time.Second,
		},
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "liveapi",
	})
	front := httptest.NewServer(srv.Handler())
	t.Cleanup(front.Close)
	return front.URL, store
}

// livePost sends body through the proxy with the real Anthropic headers.
func livePost(t *testing.T, frontURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, frontURL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", liveKey(t))
	req.Header.Set("anthropic-version", liveVersion)
	req.Header.Set("anthropic-beta", liveBeta)
	req.Header.Set("User-Agent", "headroom-go-livetest/0")

	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request through the proxy failed: %v", err)
	}
	return resp
}

// capture writes name under testdata/live when HEADROOM_CAPTURE=1. The bytes
// are written verbatim; callers redact before calling.
func capture(t *testing.T, name string, data []byte) {
	t.Helper()
	if os.Getenv("HEADROOM_CAPTURE") != "1" {
		return
	}
	dir := filepath.Join("testdata", "live")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %s (%d bytes)", path, len(data))
}

// The one live test that needs NO credential: an unauthenticated request to
// the real endpoint. It exercises the whole hop — TLS to api.anthropic.com,
// the real anthropic-beta and anthropic-version headers, the live-zone
// dispatcher on a body big enough to compress, and Anthropic's real error
// envelope coming back — and proves headroom forwards that error rather than
// substituting a 502 of its own.
func TestLiveUnauthenticatedErrorEnvelopeIsForwarded(t *testing.T) {
	front, store := liveFront(t)
	logText := compressibleLog()
	body := `{"model":"` + liveModel + `","max_tokens":16,"system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + mustJSON(t, logText) + `}]}]}`

	req, err := http.NewRequest(http.MethodPost, front+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", liveVersion)
	req.Header.Set("anthropic-beta", liveBeta)
	// A Claude Code User-Agent, so the request classifies as Subscription and
	// this test covers the mode headroom actually runs under in the wild.
	req.Header.Set("User-Agent", "claude-cli/2.0.0 (external, cli)")
	// Deliberately no x-api-key.

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request through the proxy to the real endpoint failed: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	capture(t, "unauthenticated_error_response.json", raw)
	capture(t, "unauthenticated_error_headers.txt", []byte(dumpHeaders(resp.Header)))

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "headroom:") {
		t.Fatalf("headroom substituted its own error for the upstream one: %s", raw)
	}
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("real error body did not parse as Anthropic's envelope: %v\n%s", err, raw)
	}
	if got.Type != "error" || got.Error.Type != "authentication_error" {
		t.Errorf("envelope = %+v, want type=error error.type=authentication_error", got)
	}
	if got.RequestID == "" {
		t.Error("the real error body carried no request_id")
	}
	// Upstream response headers must survive httputil.ReverseProxy.
	if resp.Header.Get("Request-Id") == "" {
		t.Errorf("Request-Id header did not survive the hop; got %v", headerNames(resp.Header))
	}
	// headroom's own headers must reach the client and must NOT have gone
	// upstream (the Rewrite hook strips them outbound).
	if resp.Header.Get("X-Headroom-Bytes-Saved") == "" {
		t.Error("no X-Headroom-Bytes-Saved header; the dispatcher did not report a saving")
	}
	// Compression ran on a Subscription-classified request, matching upstream:
	// only its Phase E passes are PAYG-gated, and headroom-go has no Phase E.
	if store.Len() == 0 {
		t.Fatal("nothing was offloaded; the large body was not compressed")
	}
	if _, ok := store.Get(ccr.ComputeKey([]byte(logText))); !ok {
		t.Error("the CCR store does not key the original text")
	}
	t.Logf("sent %d bytes; saved %s bytes; store holds %d entries",
		len(body), resp.Header.Get("X-Headroom-Bytes-Saved"), store.Len())
}

// The plain case: a small request, real beta headers, a real 200 back.
func TestLiveSmallRequestRoundTrips(t *testing.T) {
	front, _ := liveFront(t)
	body := `{"model":"` + liveModel + `","max_tokens":16,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"Reply with the single word: pong"}]}]}`

	resp := livePost(t, front, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	capture(t, "small_request.json", []byte(body))
	capture(t, "small_response.json", raw)

	var got struct {
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("real response did not parse as an Anthropic message: %v\n%s", err, raw)
	}
	if got.Type != "message" || got.Role != "assistant" {
		t.Errorf("type/role = %q/%q, want message/assistant", got.Type, got.Role)
	}
	if len(got.Content) == 0 {
		t.Error("real response carried no content blocks")
	}
	if got.Usage.InputTokens == 0 {
		t.Error("real response reported zero input tokens")
	}
	// The request-id header proves the response headers reached the client
	// through httputil.ReverseProxy without being dropped.
	if resp.Header.Get("request-id") == "" && resp.Header.Get("x-request-id") == "" {
		t.Errorf("no upstream request id header survived the hop; got %v", headerNames(resp.Header))
	}
}

// A genuine streaming completion. The response must arrive as SSE and must be
// forwarded verbatim: headroom never compresses a response (spec 9, risk 4).
func TestLiveStreamingCompletion(t *testing.T) {
	front, _ := liveFront(t)
	body := `{"model":"` + liveModel + `","max_tokens":64,"stream":true,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"Count from one to five, one word per line."}]}]}`

	resp := livePost(t, front, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var sb strings.Builder
	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line)
		sb.WriteString("\n")
		if ev, ok := strings.CutPrefix(line, "event: "); ok {
			seen[ev] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the stream failed: %v", err)
	}
	stream := sb.String()
	capture(t, "streaming_request.json", []byte(body))
	capture(t, "streaming_response.sse", []byte(stream))

	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !seen[want] {
			t.Errorf("event %q never arrived; got %v", want, sortedKeys(seen))
		}
	}
	if strings.Contains(stream, "<<ccr:") {
		t.Error("a CCR marker appeared in a RESPONSE stream; responses must never be compressed")
	}
}

// The real error shape. An unknown model is a 404 with a typed JSON error
// body; the proxy must hand it back untouched rather than substituting its
// own 502.
func TestLiveErrorResponseIsPassedThrough(t *testing.T) {
	front, _ := liveFront(t)
	body := `{"model":"claude-does-not-exist","max_tokens":16,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	resp := livePost(t, front, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an unknown model returned 200: %s", raw)
	}
	if resp.StatusCode == http.StatusBadGateway && strings.Contains(string(raw), "headroom:") {
		t.Fatalf("headroom substituted its own 502 for a real upstream error: %s", raw)
	}
	capture(t, "error_response.json", raw)

	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("real error body did not parse as Anthropic's error envelope: %v\n%s", err, raw)
	}
	if got.Type != "error" || got.Error.Type == "" {
		t.Errorf("error envelope = %+v, want type=error with a typed error.type", got)
	}
	t.Logf("real error: status=%d type=%s message=%s", resp.StatusCode, got.Error.Type, got.Error.Message)
}

// The point of the whole product against the real API: a request big enough
// to compress must be compressed AND still accepted by Anthropic. Anthropic
// sees a <<ccr:HASH>> marker it cannot dereference, so the assertion is that
// the API accepts the body — not that the model understands the marker.
func TestLiveLargeRequestIsCompressedAndAccepted(t *testing.T) {
	front, store := liveFront(t)
	logText := compressibleLog()
	body := `{"model":"` + liveModel + `","max_tokens":32,"system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + mustJSON(t, logText) +
		`}]}]}`
	if len(body) < 20000 {
		t.Fatalf("fixture is only %d bytes; too small to prove compression", len(body))
	}

	resp := livePost(t, front, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	capture(t, "large_request.json", []byte(body))
	capture(t, "large_response.json", raw)

	// Compression must actually have run: the store holds the original.
	if store.Len() == 0 {
		t.Fatal("nothing was offloaded to CCR; the large request was not compressed")
	}
	if _, ok := store.Get(ccr.ComputeKey([]byte(logText))); !ok {
		t.Error("the CCR store does not key the original log text")
	}

	var got struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("real response did not parse: %v\n%s", err, raw)
	}
	t.Logf("client sent %d bytes; Anthropic billed %d input tokens", len(body), got.Usage.InputTokens)
	if got.Usage.InputTokens == 0 {
		t.Error("Anthropic reported zero input tokens for a compressed request")
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dumpHeaders renders response headers as sorted "Name: value" lines. Only
// RESPONSE headers are captured, and headroom never sets a credential on one,
// so no fixture can carry a secret.
func dumpHeaders(h http.Header) string {
	lines := make([]string, 0, len(h))
	for k, v := range h {
		lines = append(lines, k+": "+strings.Join(v, ", "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func headerNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	return names
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
