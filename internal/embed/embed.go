// Package embed turns text into a vector by calling a local embedding model
// over HTTP. The model runs in its own process (Ollama), so the core stays
// CGO_ENABLED=0 and cross-compiles.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Defaults, per the design spec. All three are overridable config.
const (
	DefaultEndpoint = "http://localhost:11434"
	DefaultModel    = "nomic-embed-text"
	DefaultTimeout  = 2 * time.Second
)

// Client calls an Ollama-compatible /api/embeddings endpoint. HTTP.Timeout is
// the whole-call budget; the caller's ctx can only shorten it.
type Client struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
	HTTP     *http.Client
}

// New builds a Client. Empty endpoint, empty model, or non-positive timeout
// fall back to the package defaults.
func New(endpoint, model string, timeout time.Duration) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if model == "" {
		model = DefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		Endpoint: endpoint,
		Model:    model,
		Timeout:  timeout,
		HTTP:     &http.Client{Timeout: timeout},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Embed returns the vector for text. Every failure path returns an error; the
// caller treats an error as a cache miss, never as a fatal condition.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.Model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d", resp.StatusCode)
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty embedding")
	}
	return out.Embedding, nil
}
