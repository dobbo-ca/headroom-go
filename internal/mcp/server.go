// Package mcp exposes headroom's compression core over the Model Context
// Protocol on stdio. It is a thin adapter: argument validation, the
// token-aware reject (I5), CCR stashing, session counters, and result shaping.
// All compression logic lives behind router.Router.
package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/mark3labs/mcp-go/server"
)

// Deps are the collaborators the tool handlers need.
type Deps struct {
	Router     *router.Router
	Store      ccr.Store
	Tokenizer  tokenizer.Tokenizer
	ProxyURL   string
	HTTPClient *http.Client
	Version    string
}

// Stats are session-scoped counters. They carry no timestamps, so a stats
// result stays deterministic for a given call sequence (I4).
type Stats struct {
	CompressCalls int64 `json:"compress_calls"`
	RetrieveCalls int64 `json:"retrieve_calls"`
	BytesIn       int64 `json:"bytes_in"`
	BytesOut      int64 `json:"bytes_out"`
	BytesSaved    int64 `json:"bytes_saved"`
	TokensIn      int64 `json:"tokens_in"`
	TokensOut     int64 `json:"tokens_out"`
	LocalHits     int64 `json:"local_hits"`
	ProxyHits     int64 `json:"proxy_hits"`
	Misses        int64 `json:"misses"`
	StoreLen      int   `json:"store_len"`
}

// Server holds the MCP server and the counters its handlers accumulate.
type Server struct {
	deps  Deps
	mcp   *server.MCPServer
	mu    sync.Mutex
	stats Stats
}

// NewServer builds the MCP server and registers headroom's tools.
func NewServer(d Deps) *Server {
	if d.Version == "" {
		d.Version = "dev"
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	s := &Server{
		deps: d,
		mcp:  server.NewMCPServer("headroom", d.Version, server.WithToolCapabilities(false)),
	}
	s.mcp.AddTool(compressTool(), s.handleCompress)
	s.mcp.AddTool(retrieveTool(), s.handleRetrieve)
	s.mcp.AddTool(statsTool(), s.handleStats)
	return s
}

// MCP exposes the underlying server so callers can attach a transport.
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// ServeStdio runs the server on stdin/stdout until the client disconnects.
func (s *Server) ServeStdio() error { return server.ServeStdio(s.mcp) }

// writeJSON marshals v compactly with HTML escaping off, so markers such as
// <<ccr:HASH>> survive as themselves rather than as < escapes.
func writeJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
