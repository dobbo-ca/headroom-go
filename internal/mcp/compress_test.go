package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// newTestServer builds a Server over an in-memory CCR store.
func newTestServer(t *testing.T) (*Server, ccr.Store) {
	t.Helper()
	store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 100, TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("ccr.FromConfig: %v", err)
	}
	s := NewServer(Deps{
		Router:    router.NewDefault(),
		Store:     store,
		Tokenizer: tokenizer.GetTokenizer("claude"),
		ProxyURL:  "http://127.0.0.1:1", // unreachable on purpose
		Version:   "test",
	})
	return s, store
}

// callTool drives the server through a real in-process MCP client.
func callTool(t *testing.T, s *Server, name string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	c, err := client.NewInProcessClient(s.MCP())
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	var init mcpgo.InitializeRequest
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}

	var req mcpgo.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := mcpgo.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("tool result content is not text: %#v", res.Content[0])
	}
	return tc.Text
}

func decodeCompress(t *testing.T, res *mcpgo.CallToolResult) compressResult {
	t.Helper()
	var out compressResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode compress result %q: %v", resultText(t, res), err)
	}
	return out
}

// bloatedJSON is a JSON array with enough whitespace for the pipeline to shrink.
func bloatedJSON() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 60; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("    {\n        \"id\"    :    ")
		b.WriteString(strings.Repeat(" ", 8))
		b.WriteString("1234,\n        \"status\" :  \"ok\",\n        \"message\" :  \"nothing to report here at all\"\n    }")
	}
	b.WriteString("\n]\n")
	return b.String()
}

func TestCompressToolIsRegistered(t *testing.T) {
	s, _ := newTestServer(t)
	c, err := client.NewInProcessClient(s.MCP())
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var init mcpgo.InitializeRequest
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var found bool
	for _, tool := range list.Tools {
		if tool.Name == "headroom_compress" {
			found = true
		}
	}
	if !found {
		t.Fatal("headroom_compress is not registered")
	}
}

func TestCompressShrinksBloatedJSONAndStashesTheOriginal(t *testing.T) {
	s, store := newTestServer(t)
	input := bloatedJSON()

	out := decodeCompress(t, callTool(t, s, "headroom_compress", map[string]any{"content": input}))

	if len(out.Compressed) >= len(input) {
		t.Fatalf("compressed length %d did not shrink below %d", len(out.Compressed), len(input))
	}
	if out.CompressedTokens >= out.OriginalTokens {
		t.Fatalf("tokens did not shrink: %d -> %d", out.OriginalTokens, out.CompressedTokens)
	}
	if out.BytesSaved <= 0 {
		t.Fatalf("bytes_saved = %d, want > 0", out.BytesSaved)
	}
	if out.ContentType != "json_array" {
		t.Errorf("content_type = %q, want json_array", out.ContentType)
	}
	if len(out.StepsApplied) == 0 {
		t.Error("steps_applied is empty; the pipeline reported no work")
	}
	// The returned hash must resolve to the byte-exact original.
	got, ok := store.Get(out.Hash)
	if !ok {
		t.Fatalf("hash %q does not resolve in the store", out.Hash)
	}
	if got != input {
		t.Error("stored payload is not byte-equal to the original input")
	}
}

func TestCompressEmitsResolvableCacheKeys(t *testing.T) {
	s, store := newTestServer(t)
	out := decodeCompress(t, callTool(t, s, "headroom_compress", map[string]any{"content": bloatedJSON()}))
	for _, k := range out.CacheKeys {
		if _, ok := store.Get(k); !ok {
			t.Errorf("cache key %q does not resolve in the store", k)
		}
	}
}

func TestCompressRejectsInflation(t *testing.T) {
	s, _ := newTestServer(t)
	// Short prose has no registered transform, so the pipeline returns it
	// verbatim: tokens do not shrink, and I5 must report the original.
	input := "hi"
	out := decodeCompress(t, callTool(t, s, "headroom_compress", map[string]any{"content": input}))
	if out.Compressed != input {
		t.Errorf("compressed = %q, want the original %q", out.Compressed, input)
	}
	if out.BytesSaved != 0 {
		t.Errorf("bytes_saved = %d, want 0 on an I5 reject", out.BytesSaved)
	}
	if out.Hash != "" {
		t.Errorf("hash = %q, want empty on an I5 reject", out.Hash)
	}
	if len(out.StepsApplied) != 0 || len(out.CacheKeys) != 0 {
		t.Errorf("steps=%v keys=%v, want both empty on an I5 reject", out.StepsApplied, out.CacheKeys)
	}
}

func TestCompressIsDeterministic(t *testing.T) {
	input := bloatedJSON()
	s1, _ := newTestServer(t)
	s2, _ := newTestServer(t)
	a := resultText(t, callTool(t, s1, "headroom_compress", map[string]any{"content": input}))
	b := resultText(t, callTool(t, s2, "headroom_compress", map[string]any{"content": input}))
	if a != b {
		t.Errorf("I4 violated: two runs differ\nfirst:  %s\nsecond: %s", a, b)
	}
}

func TestCompressRejectsEmptyContent(t *testing.T) {
	s, _ := newTestServer(t)
	res := callTool(t, s, "headroom_compress", map[string]any{"content": ""})
	if !res.IsError {
		t.Fatal("empty content did not produce an error result")
	}
}

func TestCompressEmitsEmptyListsNotNull(t *testing.T) {
	s, _ := newTestServer(t)
	raw := resultText(t, callTool(t, s, "headroom_compress", map[string]any{"content": "hi"}))
	if strings.Contains(raw, "null") {
		t.Errorf("result contains null: %s", raw)
	}
}

func TestCompressHashIsTheContentAddressOfTheOriginal(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()
	out := decodeCompress(t, callTool(t, s, "headroom_compress", map[string]any{"content": input}))
	if want := ccr.ComputeKey([]byte(input)); out.Hash != want {
		t.Errorf("hash = %q, want the content address of the original %q", out.Hash, want)
	}
}

func TestCompressCountersMove(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()
	callTool(t, s, "headroom_compress", map[string]any{"content": input})

	s.mu.Lock()
	got := s.stats
	s.mu.Unlock()

	if got.CompressCalls != 1 {
		t.Errorf("compress_calls = %d, want 1", got.CompressCalls)
	}
	if got.BytesIn != int64(len(input)) {
		t.Errorf("bytes_in = %d, want %d", got.BytesIn, len(input))
	}
	if got.BytesOut <= 0 || got.BytesOut >= got.BytesIn {
		t.Errorf("bytes_out = %d, want between 0 and %d", got.BytesOut, got.BytesIn)
	}
	if got.BytesSaved <= 0 {
		t.Errorf("bytes_saved = %d, want > 0", got.BytesSaved)
	}
	if got.TokensIn <= 0 || got.TokensOut <= 0 || got.TokensOut >= got.TokensIn {
		t.Errorf("tokens_in = %d, tokens_out = %d", got.TokensIn, got.TokensOut)
	}
}

func TestCompressCountersAreRaceFree(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			var req mcpgo.CallToolRequest
			req.Params.Name = "headroom_compress"
			req.Params.Arguments = map[string]any{"content": input}
			if _, err := s.handleCompress(context.Background(), req); err != nil {
				t.Errorf("handleCompress: %v", err)
			}
		}()
	}
	wg.Wait()

	s.mu.Lock()
	got := s.stats.CompressCalls
	s.mu.Unlock()
	if got != n {
		t.Errorf("compress_calls = %d, want %d", got, n)
	}
}
