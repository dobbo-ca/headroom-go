package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// hashRe matches a ccr.ComputeKey output: 24 lowercase hex characters.
var hashRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// proxyClient talks to the local headroom proxy; the timeout bounds a retrieve
// when the proxy is up but wedged.
var proxyClient = &http.Client{Timeout: 5 * time.Second}

type retrieveResult struct {
	Found   bool   `json:"found"`
	Source  string `json:"source"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

func retrieveTool() mcp.Tool {
	return mcp.NewTool("headroom_retrieve",
		mcp.WithDescription("Recover the byte-exact original text behind a <<ccr:HASH>> marker left by headroom_compress. Checks the local store first, then the headroom proxy."),
		mcp.WithString("hash", mcp.Required(), mcp.Description("The 24-character hex CCR key.")),
		mcp.WithString("query", mcp.Description("Reserved; ignored in v0.1.")),
	)
}

func (s *Server) handleRetrieve(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	hash, err := req.RequireString("hash")
	if err != nil {
		return mcp.NewToolResultError("headroom_retrieve: " + err.Error()), nil
	}
	if !hashRe.MatchString(hash) {
		return mcp.NewToolResultError("headroom_retrieve: hash must be 24 lowercase hex characters"), nil
	}

	out := retrieveResult{Hash: hash, Source: "none"}
	if payload, ok := s.deps.Store.Get(hash); ok {
		out.Found, out.Source, out.Content = true, "local", payload
	} else if payload, ok := s.fetchFromProxy(ctx, hash); ok {
		out.Found, out.Source, out.Content = true, "proxy", payload
		s.deps.Store.Put(hash, payload) // a second retrieve stays local
	}

	s.recordRetrieve(out.Source)

	text, err := writeJSON(out)
	if err != nil {
		return mcp.NewToolResultError("headroom_retrieve: encode result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(text), nil
}

// fetchFromProxy asks the headroom proxy for a payload the local store lacks.
// The proxy is optional in v0.1, so every failure mode reports a plain miss.
func (s *Server) fetchFromProxy(ctx context.Context, hash string) (string, bool) {
	if s.deps.ProxyURL == "" {
		return "", false
	}
	body, err := json.Marshal(struct {
		Hash string `json:"hash"`
	}{hash})
	if err != nil {
		return "", false
	}
	url := strings.TrimRight(s.deps.ProxyURL, "/") + "/v1/retrieve"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil || decoded.Content == "" {
		return "", false
	}
	return decoded.Content, true
}

func (s *Server) recordRetrieve(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.RetrieveCalls++
	switch source {
	case "local":
		s.stats.LocalHits++
	case "proxy":
		s.stats.ProxyHits++
	default:
		s.stats.Misses++
	}
}
