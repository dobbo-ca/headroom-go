package embed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEmbedReturnsVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path = %q, want /api/embeddings", r.URL.Path)
		}
		w.Write([]byte(`{"embedding":[0.1,0.2,0.3]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	v, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(v) != 3 {
		t.Fatalf("len(v) = %d, want 3", len(v))
	}
	if v[0] != 0.1 {
		t.Errorf("v[0] = %v, want 0.1", v[0])
	}
}

func TestEmbedSendsModelAndPrompt(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// io.ReadAll, not Body.Read: a single Read may return a short buffer.
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		got = string(b)
		w.Write([]byte(`{"embedding":[1]}`))
	}))
	defer srv.Close()

	New(srv.URL, "my-model", time.Second).Embed(context.Background(), "some text")
	if !strings.Contains(got, `"model":"my-model"`) {
		t.Errorf("body %q missing model", got)
	}
	if !strings.Contains(got, `"prompt":"some text"`) {
		t.Errorf("body %q missing prompt", got)
	}
}

func TestEmbedErrorsWhenEndpointDown(t *testing.T) {
	// Port 1 is reserved and never listening.
	c := New("http://127.0.0.1:1", "test-model", 200*time.Millisecond)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error with no server")
	}
}

func TestEmbedErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error on HTTP 500")
	}
}

func TestEmbedErrorsOnEmptyVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"embedding":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", time.Second)
	if _, err := c.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed returned nil error on empty embedding")
	}
}
