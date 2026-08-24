package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A client-wide Timeout would cut long SSE streams mid-response. The deadline
// belongs on the per-request context instead.
func TestClientHasNoWideTimeout(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	if srv.client.Timeout != 0 {
		t.Errorf("http.Client.Timeout = %v, want 0: a client-wide timeout truncates SSE", srv.client.Timeout)
	}
}

// Redirects must be handed back to the client, not followed: following one
// would replay the client's credentials at an address it never chose.
func TestHealthzUpstreamDoesNotFollowRedirects(t *testing.T) {
	var targetTouched bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetTouched = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/upstream", nil))

	if targetTouched {
		t.Error("the redirect was followed; credentials must not be replayed at a redirect target")
	}
	var got struct {
		UpstreamStatus int `json:"upstream_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.UpstreamStatus != http.StatusFound {
		t.Errorf("upstream_status = %d, want 302 reported verbatim", got.UpstreamStatus)
	}
}

func TestHealthzReportsVersion(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != "test" {
		t.Errorf("version = %v, want the configured version %q", got["version"], "test")
	}
}

// The retrieve body is capped: a 24-hex hash never needs more than a few
// dozen bytes, and this is the one route that reads an unbounded client body.
func TestRetrieveRejectsOversizeBody(t *testing.T) {
	store := newMapStore()
	store.Put("000000000000000000000000", "payload")
	srv := testServer(t, store, "https://upstream.invalid")

	body := `{"hash":"000000000000000000000000","pad":"` + strings.Repeat("x", 8192) + `"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/retrieve", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: an oversize retrieve body must be rejected", rec.Code)
	}
}

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// Cancelling the context must actually drain the server: the listener stops
// accepting, and ListenAndServe returns.
func TestListenAndServeShutsDownOnCancel(t *testing.T) {
	addr := freeAddr(t)
	srv := testServer(t, nil, "https://upstream.invalid")
	srv.deps.Config.Listen = addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()

	// Wait for the listener to come up by actually serving a request.
	var served bool
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			served = resp.StatusCode == http.StatusOK
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !served {
		cancel()
		t.Fatal("the server never served /healthz on its configured Listen address")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after the context was cancelled")
	}

	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("the listener still accepts connections after shutdown")
	}
}

// A listen failure must be reported, not swallowed.
func TestListenAndServeReturnsListenError(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	srv.deps.Config.Listen = "127.0.0.1:-1"

	if err := srv.ListenAndServe(context.Background()); err == nil {
		t.Error("ListenAndServe returned nil for an unusable listen address")
	}
}
