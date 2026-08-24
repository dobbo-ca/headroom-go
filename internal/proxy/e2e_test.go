package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// The whole product in one test: a client posts a fat /v1/messages body, the
// upstream receives a smaller one carrying a CCR marker, and that marker
// resolves back to the byte-exact original through the proxy's own
// /v1/retrieve route.
func TestEndToEndCompressAndRetrieve(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	defer up.Close()

	store := newMapStore()
	srv := New(Deps{
		Config:    Config{Upstream: up.URL, MaxBodyBytes: 32 << 20, Compress: true, RequestTimeout: 30e9, DialTimeout: 10e9},
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "e2e",
	})
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	logText := compressibleLog()
	original := messagesBody(t, logText)

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// 1. The upstream received less than the client sent.
	if len(upstreamBody) >= len(original) {
		t.Fatalf("upstream got %d bytes, client sent %d: no compression happened",
			len(upstreamBody), len(original))
	}
	t.Logf("client sent %d bytes, upstream received %d (%.1f%% cut)",
		len(original), len(upstreamBody),
		100*(1-float64(len(upstreamBody))/float64(len(original))))

	// 2. What the upstream received is still valid JSON.
	if !json.Valid([]byte(upstreamBody)) {
		t.Fatal("the body sent upstream is not valid JSON")
	}

	// 3. The frozen prefix survived byte-for-byte.
	for _, frag := range []string{`"system":"you are helpful"`, `"model":"claude-3-5-sonnet-20241022"`} {
		if !strings.Contains(upstreamBody, frag) {
			t.Errorf("frozen prefix fragment altered: %s", frag)
		}
	}

	// 4. A CCR marker is present and resolves through /v1/retrieve.
	hash := markerHashIn(t, upstreamBody)
	rresp, err := http.Post(front.URL+"/v1/retrieve", "application/json",
		strings.NewReader(`{"hash":"`+hash+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/retrieve status = %d", rresp.StatusCode)
	}
	var got struct {
		Found   bool   `json:"found"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(rresp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Found {
		t.Fatal("the emitted CCR marker did not resolve")
	}
	if got.Content != logText {
		t.Error("the retrieved payload is not the byte-exact original")
	}

	// 5. The hash the marker carries is the key of the original.
	if hash != ccr.ComputeKey([]byte(logText)) {
		t.Error("the marker hash is not the CCR key of the original text")
	}
}

// markerHashIn extracts the single <<ccr:HASH>> hash present in s.
func markerHashIn(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "<<ccr:")
	if i < 0 {
		t.Fatalf("no CCR marker found in the upstream body")
	}
	rest := s[i+len("<<ccr:"):]
	j := strings.Index(rest, ">>")
	if j < 0 {
		t.Fatalf("unterminated CCR marker")
	}
	return rest[:j]
}

// twoMessageBody builds a /v1/messages body whose FIRST user message carries a
// cache_control marker — pinning it into Anthropic's cached prefix — and whose
// second user message is live. Both texts are equally compressible, so the only
// thing that can keep the first one intact is the frozen floor.
func twoMessageBody(t *testing.T, frozenText, liveText string) string {
	t.Helper()
	frozen, err := json.Marshal(frozenText)
	if err != nil {
		t.Fatal(err)
	}
	live, err := json.Marshal(liveText)
	if err != nil {
		t.Fatal(err)
	}
	return `{"model":"claude-3-5-sonnet-20241022","system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + string(frozen) +
		`,"cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":` + string(live) + `}]}]}`
}

// The proxy must honour the customer's cache_control frozen prefix: touching a
// byte below the floor changes Anthropic's cache key and silently inflates the
// bill. The live message must still be compressed, so this cannot pass by the
// proxy simply doing nothing.
func TestEndToEndFrozenPrefixIsNotTouched(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	defer up.Close()

	srv := New(Deps{
		Config:    Config{Upstream: up.URL, MaxBodyBytes: 32 << 20, Compress: true, RequestTimeout: 30e9, DialTimeout: 10e9},
		Store:     newMapStore(),
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "e2e",
	})
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	frozenText := compressibleLog()
	liveText := strings.ReplaceAll(compressibleLog(), "worker", "runner")
	original := twoMessageBody(t, frozenText, liveText)

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// The live message was compressed: the request did get smaller, and the
	// marker resolves to the LIVE text, not the frozen one.
	if len(upstreamBody) >= len(original) {
		t.Fatalf("upstream got %d bytes, client sent %d: no compression happened",
			len(upstreamBody), len(original))
	}
	hash := markerHashIn(t, upstreamBody)
	if hash != ccr.ComputeKey([]byte(liveText)) {
		t.Errorf("the emitted marker keys %q; want the LIVE message %q",
			hash, ccr.ComputeKey([]byte(liveText)))
	}
	if hash == ccr.ComputeKey([]byte(frozenText)) {
		t.Fatal("the FROZEN message was offloaded to CCR")
	}

	// The frozen message survived byte-for-byte, marker and all.
	encodedFrozen, err := json.Marshal(frozenText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upstreamBody, string(encodedFrozen)) {
		t.Error("the cache_control-frozen message was rewritten; the prompt cache key is broken")
	}
	if !strings.Contains(upstreamBody, `"cache_control":{"type":"ephemeral"}`) {
		t.Error("the cache_control marker itself did not survive")
	}
	// Exactly one block was offloaded, so exactly one marker is present.
	if n := strings.Count(upstreamBody, "<<ccr:"); n != 1 {
		t.Errorf("found %d CCR markers upstream, want exactly 1 (the live message)", n)
	}
}

// When the customer pins the WHOLE prompt with a cache_control marker on the
// last message, there is no live zone: headroom must forward the request byte
// for byte. Rewriting anything here changes Anthropic's cache key, drops the
// hit rate to zero, and inflates the customer's bill.
func TestEndToEndFullyCachedPromptIsForwardedVerbatim(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	defer up.Close()

	store := newMapStore()
	srv := New(Deps{
		Config:    Config{Upstream: up.URL, MaxBodyBytes: 32 << 20, Compress: true, RequestTimeout: 30e9, DialTimeout: 10e9},
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "e2e",
	})
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	quoted, err := json.Marshal(compressibleLog())
	if err != nil {
		t.Fatal(err)
	}
	// Both messages are cache-pinned, so the frozen floor covers every one.
	original := `{"model":"claude-3-5-sonnet-20241022","system":"you are helpful","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]},` +
		`{"role":"user","content":[{"type":"text","text":` + string(quoted) +
		`,"cache_control":{"type":"ephemeral"}}]}]}`

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	if upstreamBody != original {
		t.Errorf("a fully cache-pinned body was rewritten: upstream got %d bytes, client sent %d",
			len(upstreamBody), len(original))
	}
	if strings.Contains(upstreamBody, "<<ccr:") {
		t.Error("a CCR marker was injected into a fully cache-pinned prompt")
	}
	if store.Len() != 0 {
		t.Errorf("store holds %d entries; a fully cache-pinned prompt must offload nothing", store.Len())
	}
}
