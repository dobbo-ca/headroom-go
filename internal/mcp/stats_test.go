package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func decodeStats(t *testing.T, res *mcpgo.CallToolResult) Stats {
	t.Helper()
	var out Stats
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode stats result: %v", err)
	}
	return out
}

func TestStatsStartAtZero(t *testing.T) {
	s, _ := newTestServer(t)
	got := decodeStats(t, callTool(t, s, "headroom_stats", map[string]any{}))
	if got.CompressCalls != 0 || got.RetrieveCalls != 0 || got.BytesSaved != 0 {
		t.Errorf("fresh server stats are not zero: %+v", got)
	}
}

func TestStatsCountCompressWork(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()
	callTool(t, s, "headroom_compress", map[string]any{"content": input})

	got := decodeStats(t, callTool(t, s, "headroom_stats", map[string]any{}))
	if got.CompressCalls != 1 {
		t.Errorf("compress_calls = %d, want 1", got.CompressCalls)
	}
	if got.BytesIn != int64(len(input)) {
		t.Errorf("bytes_in = %d, want %d", got.BytesIn, len(input))
	}
	if got.BytesOut >= got.BytesIn {
		t.Errorf("bytes_out %d did not fall below bytes_in %d", got.BytesOut, got.BytesIn)
	}
	if got.TokensIn <= 0 || got.TokensOut >= got.TokensIn {
		t.Errorf("tokens did not shrink: in=%d out=%d", got.TokensIn, got.TokensOut)
	}
	if got.StoreLen <= 0 {
		t.Errorf("store_len = %d, want > 0 after a compress that stashed an original", got.StoreLen)
	}
}

func TestStatsCountRetrieveOutcomes(t *testing.T) {
	s, store := newTestServer(t)
	payload := "stashed"
	hash := ccr.ComputeKey([]byte(payload))
	store.Put(hash, payload)

	// Hits and misses differ in count so a swapped pair of counters cannot pass.
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash})                       // local hit
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash})                       // local hit
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": "ffffffffffffffffffffffff"}) // miss

	got := decodeStats(t, callTool(t, s, "headroom_stats", map[string]any{}))
	if got.RetrieveCalls != 3 {
		t.Errorf("retrieve_calls = %d, want 3", got.RetrieveCalls)
	}
	if got.LocalHits != 2 {
		t.Errorf("local_hits = %d, want 2", got.LocalHits)
	}
	if got.Misses != 1 {
		t.Errorf("misses = %d, want 1", got.Misses)
	}
	if got.ProxyHits != 0 {
		t.Errorf("proxy_hits = %d, want 0", got.ProxyHits)
	}
}

func TestStatsAreDeterministicForTheSameCallSequence(t *testing.T) {
	run := func() string {
		s, _ := newTestServer(t)
		callTool(t, s, "headroom_compress", map[string]any{"content": bloatedJSON()})
		return resultText(t, callTool(t, s, "headroom_stats", map[string]any{}))
	}
	if a, b := run(), run(); a != b {
		t.Errorf("I4 violated: stats differ across identical runs\n%s\n%s", a, b)
	}
}

func TestStatsSnapshotIsRaceFree(t *testing.T) {
	s, _ := newTestServer(t)
	input := bloatedJSON()

	const n = 8
	done := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
					s.snapshot()
				}
			}
		}()
	}

	var writers sync.WaitGroup
	writers.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer writers.Done()
			var req mcpgo.CallToolRequest
			req.Params.Name = "headroom_compress"
			req.Params.Arguments = map[string]any{"content": input}
			if _, err := s.handleCompress(context.Background(), req); err != nil {
				t.Errorf("handleCompress: %v", err)
			}
		}()
	}
	writers.Wait()
	close(done)
	readers.Wait()

	if got := s.snapshot(); got.CompressCalls != n {
		t.Errorf("compress_calls = %d, want %d", got.CompressCalls, n)
	}
}
