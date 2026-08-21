package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

func statsTool() mcp.Tool {
	return mcp.NewTool("headroom_stats",
		mcp.WithDescription("Report this session's compression counters: calls, bytes in and out, tokens in and out, and CCR hit rates. Session-scoped; it does not aggregate across processes."),
	)
}

func (s *Server) handleStats(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := writeJSON(s.snapshot())
	if err != nil {
		return mcp.NewToolResultError("headroom_stats: encode result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(text), nil
}

// snapshot copies the counters under the mutex. Store.Len is read outside the
// mutex because the store has its own lock and is not part of Server's state.
func (s *Server) snapshot() Stats {
	storeLen := s.deps.Store.Len()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.StoreLen = storeLen
	return out
}
