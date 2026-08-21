package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func decodeRetrieve(t *testing.T, res *mcpgo.CallToolResult) retrieveResult {
	t.Helper()
	var out retrieveResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode retrieve result: %v", err)
	}
	return out
}

// proxyServer builds a Server whose proxy is h.
func proxyServer(t *testing.T, h http.HandlerFunc) *Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 100, TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	return NewServer(Deps{
		Router:    router.NewDefault(),
		Store:     store,
		Tokenizer: tokenizer.GetTokenizer("claude"),
		ProxyURL:  srv.URL,
		Version:   "test",
	})
}

func TestRetrieveLocalHit(t *testing.T) {
	s, store := newTestServer(t)
	payload := "the original tool output"
	hash := ccr.ComputeKey([]byte(payload))
	store.Put(hash, payload)

	out := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash}))
	if !out.Found || out.Source != "local" {
		t.Fatalf("got found=%v source=%q, want true/local", out.Found, out.Source)
	}
	if out.Content != payload {
		t.Errorf("content = %q, want %q", out.Content, payload)
	}
}

func TestRetrieveRoundTripsACompressHash(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()
	c := decodeCompress(t, callTool(t, s, "headroom_compress", map[string]any{"content": input}))
	if c.Hash == "" {
		t.Fatal("compress returned no hash")
	}
	r := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": c.Hash}))
	if !r.Found {
		t.Fatal("a hash emitted by headroom_compress did not resolve")
	}
	if r.Content != input {
		t.Error("retrieved content is not byte-equal to the compressed original")
	}
}

func TestRetrieveProxyFallbackAndLocalCaching(t *testing.T) {
	payload := "payload that only the proxy has"
	hash := ccr.ComputeKey([]byte(payload))

	var calls int
	var gotBody string
	s := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/retrieve" {
			t.Errorf("proxy path = %q, want /v1/retrieve", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("proxy method = %q, want POST", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"content": payload})
	})

	out := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash}))
	if !out.Found || out.Source != "proxy" {
		t.Fatalf("got found=%v source=%q, want true/proxy", out.Found, out.Source)
	}
	if out.Content != payload {
		t.Errorf("content = %q, want %q", out.Content, payload)
	}
	if !strings.Contains(gotBody, hash) {
		t.Errorf("proxy request body %q does not carry the hash", gotBody)
	}

	// The proxy payload is cached locally, so the second call is a local hit.
	again := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash}))
	if again.Source != "local" {
		t.Errorf("second retrieve source = %q, want local", again.Source)
	}
	if calls != 1 {
		t.Errorf("proxy was called %d times, want 1", calls)
	}
}

func TestRetrieveMissIsAResultNotAnError(t *testing.T) {
	// newTestServer points ProxyURL at an unreachable address.
	s, _ := newTestServer(t)
	res := callTool(t, s, "headroom_retrieve", map[string]any{"hash": "aabbccddeeff001122334455"})
	if res.IsError {
		t.Fatal("a miss produced an error result; it must be found=false")
	}
	out := decodeRetrieve(t, res)
	if out.Found || out.Source != "none" {
		t.Errorf("got found=%v source=%q, want false/none", out.Found, out.Source)
	}
}

func TestRetrieveRejectsMalformedHash(t *testing.T) {
	s, _ := newTestServer(t)
	for _, bad := range []string{"", "not-hex", "AABBCCDDEEFF001122334455", "aabb", strings.Repeat("a", 25)} {
		res := callTool(t, s, "headroom_retrieve", map[string]any{"hash": bad})
		if !res.IsError {
			t.Errorf("hash %q was accepted; want an error result", bad)
		}
	}
}

func TestRetrieveIgnoresNon200Proxy(t *testing.T) {
	// The body is a well-formed hit, so only the status check can reject it.
	s := proxyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"content": "a payload behind a 500"})
	})
	out := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": "aabbccddeeff001122334455"}))
	if out.Found {
		t.Error("a 500 from the proxy was treated as a hit")
	}
}

func TestRetrieveIgnoresEmptyProxyContent(t *testing.T) {
	s := proxyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"content": ""})
	})
	out := decodeRetrieve(t, callTool(t, s, "headroom_retrieve", map[string]any{"hash": "aabbccddeeff001122334455"}))
	if out.Found || out.Source != "none" {
		t.Errorf("empty proxy content was treated as a hit: found=%v source=%q", out.Found, out.Source)
	}
}

func TestRetrieveIsDeterministic(t *testing.T) {
	s1, store1 := newTestServer(t)
	s2, store2 := newTestServer(t)
	payload := "the original tool output"
	hash := ccr.ComputeKey([]byte(payload))
	store1.Put(hash, payload)
	store2.Put(hash, payload)

	a := resultText(t, callTool(t, s1, "headroom_retrieve", map[string]any{"hash": hash}))
	b := resultText(t, callTool(t, s2, "headroom_retrieve", map[string]any{"hash": hash}))
	if a != b {
		t.Errorf("I4 violated: two runs differ\nfirst:  %s\nsecond: %s", a, b)
	}
	miss := resultText(t, callTool(t, s1, "headroom_retrieve", map[string]any{"hash": "aabbccddeeff001122334455"}))
	if again := resultText(t, callTool(t, s2, "headroom_retrieve", map[string]any{"hash": "aabbccddeeff001122334455"})); miss != again {
		t.Errorf("I4 violated on a miss:\nfirst:  %s\nsecond: %s", miss, again)
	}
}

func TestRetrieveCountersMove(t *testing.T) {
	payload := "payload that only the proxy has"
	hash := ccr.ComputeKey([]byte(payload))
	// The proxy knows this one hash and nothing else.
	s := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Hash string `json:"hash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Hash != hash {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"content": payload})
	})

	callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash})                       // proxy hit
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash})                       // now cached: local hit
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": "000000000000000000000000"}) // unknown to the proxy: miss

	s.mu.Lock()
	got := s.stats
	s.mu.Unlock()

	if got.RetrieveCalls != 3 {
		t.Errorf("retrieve_calls = %d, want 3", got.RetrieveCalls)
	}
	if got.ProxyHits != 1 {
		t.Errorf("proxy_hits = %d, want 1", got.ProxyHits)
	}
	if got.LocalHits != 1 {
		t.Errorf("local_hits = %d, want 1", got.LocalHits)
	}
	if got.Misses != 1 {
		t.Errorf("misses = %d, want 1", got.Misses)
	}
}

func TestRetrieveCountersAreRaceFree(t *testing.T) {
	s, store := newTestServer(t)
	payload := "the original tool output"
	hash := ccr.ComputeKey([]byte(payload))
	store.Put(hash, payload)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			var req mcpgo.CallToolRequest
			req.Params.Name = "headroom_retrieve"
			req.Params.Arguments = map[string]any{"hash": hash}
			if _, err := s.handleRetrieve(context.Background(), req); err != nil {
				t.Errorf("handleRetrieve: %v", err)
			}
		}()
	}
	wg.Wait()

	s.mu.Lock()
	got := s.stats.RetrieveCalls
	s.mu.Unlock()
	if got != n {
		t.Errorf("retrieve_calls = %d, want %d", got, n)
	}
}
