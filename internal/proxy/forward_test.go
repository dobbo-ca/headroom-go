package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// compressibleLog is a repetitive log body the log compressor reliably shrinks.
func compressibleLog() string {
	var b strings.Builder
	b.WriteString("FAILED build with 1 error\n")
	for i := 0; i < 300; i++ {
		b.WriteString("2026-01-01 00:00:00 INFO  worker: processed batch id=000000 status=ok latency_ms=12\n")
	}
	return b.String()
}

func messagesBody(t *testing.T, text string) string {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return `{"model":"claude-3-5-sonnet-20241022","system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + string(quoted) + `}]}]}`
}

// captureUpstream records what the proxy actually sent.
type captured struct {
	path   string
	method string
	body   string
	host   string
	header http.Header
}

func upstreamCapturing(t *testing.T, got *captured, reply func(http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.path, got.method, got.body = r.URL.RequestURI(), r.Method, string(b)
		got.host, got.header = r.Host, r.Header.Clone()
		reply(w)
	}))
}

func TestForwardCompressesMessagesRequestBody(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	original := messagesBody(t, compressibleLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(original))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sk-ant-api03-test")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if len(got.body) >= len(original) {
		t.Errorf("upstream got %d bytes, not smaller than the %d-byte original", len(got.body), len(original))
	}
	if !json.Valid([]byte(got.body)) {
		t.Error("the compressed body sent upstream is not valid JSON")
	}
	if got.path != "/v1/messages" {
		t.Errorf("upstream path = %q", got.path)
	}
	if !strings.Contains(got.body, "<<ccr:") {
		t.Error("no CCR marker in the compressed body")
	}
}

// A path the dispatcher does not handle must forward byte-identically.
func TestForwardLeavesNonMessagesBodyByteIdentical(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	original := messagesBody(t, compressibleLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
	srv.Handler().ServeHTTP(rec, req)

	if got.body != original {
		t.Error("a non-messages body was modified")
	}
}

// Compression disabled means byte-identical forwarding, even on /v1/messages.
func TestForwardCompressDisabledIsByteIdentical(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	srv.deps.Config.Compress = false
	original := messagesBody(t, compressibleLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(original))
	srv.Handler().ServeHTTP(rec, req)

	if got.body != original {
		t.Error("compression ran with Compress disabled")
	}
}

// THE RESPONSE MUST NEVER BE COMPRESSED. A highly compressible response body
// must come back to the client byte-for-byte.
func TestForwardNeverCompressesTheResponse(t *testing.T) {
	responseBody := compressibleLog()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(messagesBody(t, compressibleLog())))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Body.String() != responseBody {
		t.Errorf("the response body was modified: got %d bytes, want %d",
			rec.Body.Len(), len(responseBody))
	}
	if strings.Contains(rec.Body.String(), "<<ccr:") {
		t.Error("a CCR marker appeared in a RESPONSE; responses must never be compressed")
	}
}

func TestForwardPropagatesStatusAndHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream-Thing", "kept")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewing"))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if rec.Header().Get("X-Upstream-Thing") != "kept" {
		t.Error("an upstream header was dropped")
	}
	if rec.Header().Get("Transfer-Encoding") != "" {
		t.Error("a hop-by-hop response header was forwarded")
	}
	if rec.Body.String() != "brewing" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

// Credentials must reach the upstream; XFF must be appended; headroom's own
// headers must not leak.
func TestForwardHeaderHandling(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Api-Key", "sk-ant-api03-secret")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("X-Headroom-Internal", "leak")
	req.RemoteAddr = "203.0.113.9:5555"

	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got.header.Get("X-Api-Key") != "sk-ant-api03-secret" {
		t.Error("credentials did not reach the upstream")
	}
	if got.header.Get("Anthropic-Version") != "2023-06-01" {
		t.Error("Anthropic-Version was dropped")
	}
	if got.header.Get("X-Headroom-Internal") != "" {
		t.Error("an x-headroom-* header leaked upstream")
	}
	if got.header.Get("X-Forwarded-For") != "203.0.113.9" {
		t.Errorf("X-Forwarded-For = %q, want the client IP", got.header.Get("X-Forwarded-For"))
	}
}

func TestForwardOversizeBodyIs413(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an oversize request must not reach the upstream")
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	srv.deps.Config.MaxBodyBytes = 64

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 4096)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestForwardUpstreamErrorIs502(t *testing.T) {
	srv := testServer(t, nil, "http://127.0.0.1:1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// An unparseable body must still be forwarded: it is the upstream's job to
// reject it, and the proxy must never lose a request.
func TestForwardInvalidJSONStillForwarded(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{not json")))

	if got.body != "{not json" {
		t.Errorf("upstream got %q, want the original bytes", got.body)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the upstream's 200", rec.Code)
	}
}

// The query string must survive.
func TestForwardPreservesQueryString(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	srv.Handler().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/v1/models?limit=5&after=abc", nil))

	if got.path != "/v1/models?limit=5&after=abc" {
		t.Errorf("upstream RequestURI = %q", got.path)
	}
}

// A compressed request must carry the new Content-Length, not the old one.
func TestForwardSetsContentLengthAfterCompression(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	body := messagesBody(t, compressibleLog())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "sk-ant-api03-x")
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if len(got.body) >= len(body) {
		t.Fatalf("compression did not run: upstream got %d bytes of a %d-byte body", len(got.body), len(body))
	}
	// The header must be present: sending the body chunked instead loses the
	// declared length the upstream expects.
	if want := strconv.Itoa(len(got.body)); got.header.Get("Content-Length") != want {
		t.Errorf("Content-Length = %q, want %q (the compressed length)",
			got.header.Get("Content-Length"), want)
	}
}

// The proxy describes its own work back to the client. These headers never
// cross the upstream boundary, so they are only ever on the response.
func TestForwardReportsTelemetryHeaders(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	original := messagesBody(t, compressibleLog())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(original))
	req.Header.Set("X-Api-Key", "sk-ant-api03-x")
	srv.Handler().ServeHTTP(rec, req)

	if want := strconv.Itoa(len(original) - len(got.body)); rec.Header().Get("X-Headroom-Bytes-Saved") != want {
		t.Errorf("X-Headroom-Bytes-Saved = %q, want %q",
			rec.Header().Get("X-Headroom-Bytes-Saved"), want)
	}
	before, err := strconv.Atoi(rec.Header().Get("X-Headroom-Tokens-Before"))
	if err != nil {
		t.Fatalf("X-Headroom-Tokens-Before = %q: %v", rec.Header().Get("X-Headroom-Tokens-Before"), err)
	}
	after, err := strconv.Atoi(rec.Header().Get("X-Headroom-Tokens-After"))
	if err != nil {
		t.Fatalf("X-Headroom-Tokens-After = %q: %v", rec.Header().Get("X-Headroom-Tokens-After"), err)
	}
	if after >= before {
		t.Errorf("tokens after = %d, want fewer than the %d before", after, before)
	}
}

// A request that never gets a response must not hang forever: the per-request
// context deadline is the only timeout on this path.
func TestForwardAppliesRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer up.Close()
	defer close(release)

	srv := testServer(t, nil, up.URL)
	srv.deps.Config.RequestTimeout = 50 * time.Millisecond

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))
		done <- rec.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502 after the request deadline", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request outlived its RequestTimeout")
	}
}

// Hop-by-hop headers (RFC 7230 6.1) must not cross the proxy, including the
// ones the client names inside Connection. Credentials must survive.
func TestForwardDropsHopByHopRequestHeaders(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Connection", "X-Custom-Hop")
	req.Header.Set("X-Custom-Hop", "secret")
	req.Header.Set("Authorization", "Bearer sk-ant-api03-x")
	// A name that merely starts with a hop-by-hop name is a distinct header;
	// Upgrade-Insecure-Requests is a real browser header, not an Upgrade.
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got.header.Get("X-Custom-Hop") != "" {
		t.Error("a Connection-listed header was forwarded")
	}
	if got.header.Get("Connection") != "" {
		t.Error("Connection itself was forwarded")
	}
	if got.header.Get("Authorization") != "Bearer sk-ant-api03-x" {
		t.Error("Authorization was not forwarded")
	}
	if got.header.Get("Upgrade-Insecure-Requests") != "1" {
		t.Error("Upgrade-Insecure-Requests was dropped; the hop-by-hop list must match whole names")
	}
}

// The client's Host must not be copied through: the upstream has to see its
// own host or TLS/routing breaks.
func TestForwardSendsTheUpstreamHost(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Host = "evil.example"
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if got.host == "evil.example" {
		t.Error("the client's Host was forwarded upstream")
	}
	if want := strings.TrimPrefix(up.URL, "http://"); got.host != want {
		t.Errorf("upstream Host = %q, want %q", got.host, want)
	}
}

// Multi-valued headers must survive intact; a naive Get/Set copy loses values.
func TestForwardPreservesMultiValuedHeaders(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Add("X-Multi", "a")
	req.Header.Add("X-Multi", "b")
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if v := got.header.Values("X-Multi"); len(v) != 2 || v[0] != "a" || v[1] != "b" {
		t.Errorf("X-Multi = %v, want [a b]", v)
	}
}

// A forwarding chain the client already started must be kept, with our
// client's IP appended.
func TestForwardAppendsToAnExistingForwardedForChain(t *testing.T) {
	var got captured
	up := upstreamCapturing(t, &got, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) })
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	req.RemoteAddr = "203.0.113.9:5555"
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if want := "198.51.100.1, 203.0.113.9"; got.header.Get("X-Forwarded-For") != want {
		t.Errorf("X-Forwarded-For = %q, want %q", got.header.Get("X-Forwarded-For"), want)
	}
}

// The upstream may declare its own hop-by-hop headers in Connection; those
// are for this hop only and must not reach the client. Content-Length must be
// kept so the client sees the real body length.
func TestForwardDropsHopByHopResponseHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "X-Upstream-Hop")
		w.Header().Set("X-Upstream-Hop", "internal")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))

	if rec.Header().Get("X-Upstream-Hop") != "" {
		t.Error("a Connection-listed response header was copied to the client")
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(`{"ok":true}`)) {
		t.Errorf("Content-Length = %q, want the real body length", rec.Header().Get("Content-Length"))
	}
}
