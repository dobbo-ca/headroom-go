package mcp

import (
	"encoding/json"
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

	callTool(t, s, "headroom_retrieve", map[string]any{"hash": hash})                       // local hit
	callTool(t, s, "headroom_retrieve", map[string]any{"hash": "ffffffffffffffffffffffff"}) // miss

	got := decodeStats(t, callTool(t, s, "headroom_stats", map[string]any{}))
	if got.RetrieveCalls != 2 {
		t.Errorf("retrieve_calls = %d, want 2", got.RetrieveCalls)
	}
	if got.LocalHits != 1 {
		t.Errorf("local_hits = %d, want 1", got.LocalHits)
	}
	if got.Misses != 1 {
		t.Errorf("misses = %d, want 1", got.Misses)
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
