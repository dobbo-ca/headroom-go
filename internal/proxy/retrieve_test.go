package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// mapStore is a minimal ccr.Store for tests.
type mapStore struct{ m map[string]string }

func newMapStore() *mapStore { return &mapStore{m: map[string]string{}} }

func (s *mapStore) Put(hash, payload string) { s.m[hash] = payload }
func (s *mapStore) Get(hash string) (string, bool) {
	v, ok := s.m[hash]
	return v, ok
}
func (s *mapStore) Len() int { return len(s.m) }

func testServer(t *testing.T, store ccr.Store, upstream string) *Server {
	t.Helper()
	if store == nil {
		store = newMapStore()
	}
	return New(Deps{
		Config:    Config{Upstream: upstream, MaxBodyBytes: 1 << 20, Compress: true},
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "test",
	})
}

func TestRetrieveHit(t *testing.T) {
	store := newMapStore()
	original := "the original payload"
	hash := ccr.ComputeKey([]byte(original))
	store.Put(hash, original)

	srv := testServer(t, store, "https://upstream.invalid")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/retrieve",
		strings.NewReader(`{"hash":"`+hash+`"}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	var got struct {
		Found   bool   `json:"found"`
		Hash    string `json:"hash"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Content != original || got.Hash != hash {
		t.Errorf("got %+v, want the original payload", got)
	}
}

func TestRetrieveMissIs404(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/retrieve",
		strings.NewReader(`{"hash":"000000000000000000000000"}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var got struct {
		Found bool `json:"found"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Found {
		t.Error("found must be false on a miss")
	}
}

func TestRetrieveRejectsBadHash(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	for _, body := range []string{
		`{"hash":"NOTHEX"}`,
		`{"hash":"ABCDEF012345678901234567"}`, // uppercase
		`{"hash":"abc"}`,                      // too short
		`{"hash":""}`,
		`{}`,
		`not json`,
	} {
		t.Run(body, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/retrieve", strings.NewReader(body))
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// /v1/retrieve is headroom's own route and must never reach the upstream.
// Pointing the upstream at a server that fails the test if touched proves it.
func TestRetrieveIsNeverForwarded(t *testing.T) {
	var touched bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		touched = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/retrieve",
		strings.NewReader(`{"hash":"000000000000000000000000"}`))
	srv.Handler().ServeHTTP(rec, req)

	if touched {
		t.Fatal("/v1/retrieve was forwarded upstream; it must be served locally")
	}
	if rec.Code == http.StatusTeapot {
		t.Fatal("the upstream response reached the client")
	}
}

func TestHealthz(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Errorf("status field = %v", got["status"])
	}
}

func TestHealthzUpstreamReportsFailure(t *testing.T) {
	srv := testServer(t, nil, "http://127.0.0.1:1") // nothing listens there
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/upstream", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when the upstream is unreachable", rec.Code)
	}
}

func TestHealthzUpstreamReportsSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/upstream", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
