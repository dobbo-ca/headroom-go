package mcp

import (
	"context"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/transform"
	"github.com/mark3labs/mcp-go/mcp"
)

type compressResult struct {
	Compressed       string   `json:"compressed"`
	Hash             string   `json:"hash"`
	ContentType      string   `json:"content_type"`
	OriginalTokens   int      `json:"original_tokens"`
	CompressedTokens int      `json:"compressed_tokens"`
	BytesSaved       int      `json:"bytes_saved"`
	StepsApplied     []string `json:"steps_applied"`
	CacheKeys        []string `json:"cache_keys"`
}

func compressTool() mcp.Tool {
	return mcp.NewTool("headroom_compress",
		mcp.WithDescription("Compress a tool output, log, diff, or JSON array before it reaches the model. Returns the compressed text plus a hash that headroom_retrieve resolves back to the byte-exact original."),
		mcp.WithString("content", mcp.Required(), mcp.Description("The text to compress.")),
		mcp.WithString("query", mcp.Description("Optional relevance hint; steers which lines survive.")),
	)
}

func (s *Server) handleCompress(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("headroom_compress: " + err.Error()), nil
	}
	if content == "" {
		return mcp.NewToolResultError("headroom_compress: content must not be empty"), nil
	}
	query := req.GetString("query", "")

	originalTokens := s.deps.Tokenizer.CountText(content)
	res := s.deps.Router.Compress(content, transform.CompressionContext{Query: query}, s.deps.Store)
	compressedTokens := s.deps.Tokenizer.CountText(res.Output)

	out := compressResult{
		ContentType:      s.deps.Router.Detect(content).Type.String(),
		OriginalTokens:   originalTokens,
		CompressedTokens: compressedTokens,
		StepsApplied:     []string{},
		CacheKeys:        []string{},
	}

	// I5: never hand back something that costs more than the input.
	if compressedTokens >= originalTokens {
		out.Compressed = content
		out.CompressedTokens = originalTokens
	} else {
		out.Compressed = res.Output
		out.BytesSaved = res.BytesSaved
		if res.StepsApplied != nil {
			out.StepsApplied = res.StepsApplied
		}
		if res.CacheKeys != nil {
			out.CacheKeys = res.CacheKeys
		}
		// Stash the original so the reported hash resolves byte for byte.
		out.Hash = ccr.ComputeKey([]byte(content))
		s.deps.Store.Put(out.Hash, content)
	}

	s.recordCompress(len(content), len(out.Compressed), out.BytesSaved, originalTokens, out.CompressedTokens)

	text, err := writeJSON(out)
	if err != nil {
		return mcp.NewToolResultError("headroom_compress: encode result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(text), nil
}

func (s *Server) recordCompress(bytesIn, bytesOut, bytesSaved, tokensIn, tokensOut int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.CompressCalls++
	s.stats.BytesIn += int64(bytesIn)
	s.stats.BytesOut += int64(bytesOut)
	s.stats.BytesSaved += int64(bytesSaved)
	s.stats.TokensIn += int64(tokensIn)
	s.stats.TokensOut += int64(tokensOut)
}
