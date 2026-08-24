package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsStreaming(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"TEXT/EVENT-STREAM", true},
		{"application/json", false},
		{"", false},
		{"text/plain", false},
	}
	for _, tt := range tests {
		t.Run(tt.ct, func(t *testing.T) {
			h := http.Header{}
			if tt.ct != "" {
				h.Set("Content-Type", tt.ct)
			}
			if got := isStreaming(h); got != tt.want {
				t.Errorf("isStreaming(%q) = %v, want %v", tt.ct, got, tt.want)
			}
		})
	}
}

// flushRecorder counts Flush calls and records the bytes visible at each one.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes   int
	snapshots []string
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {
	f.flushes++
	f.snapshots = append(f.snapshots, f.Body.String())
}

// An SSE response must be flushed as it arrives, not buffered to the end.
// The upstream writes three events with a gap between them; if the proxy
// buffered, every snapshot would be identical.
func TestStreamingResponseIsFlushedPerChunk(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("test upstream cannot flush")
			return
		}
		for _, ev := range []string{
			"event: message_start\ndata: {\"a\":1}\n\n",
			"event: content_block_delta\ndata: {\"b\":2}\n\n",
			"event: message_stop\ndata: {}\n\n",
		} {
			_, _ = w.Write([]byte(ev))
			fl.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := newFlushRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))

	if rec.flushes < 2 {
		t.Errorf("Flush called %d times; an SSE stream must be flushed per chunk", rec.flushes)
	}
	// Prove the body grew across flushes rather than appearing all at once.
	if len(rec.snapshots) >= 2 && rec.snapshots[0] == rec.snapshots[len(rec.snapshots)-1] {
		t.Error("every flush saw the same bytes; the response was buffered")
	}
	body := rec.Body.String()
	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(body, want) {
			t.Errorf("event %q missing from the streamed body", want)
		}
	}
}

// Streamed bytes must be byte-identical, including multi-byte runes split
// across chunk boundaries. We never decode, so a split rune is a non-issue —
// this test pins that we never start.
func TestStreamingPreservesBytesAcrossChunkBoundaries(t *testing.T) {
	full := "data: café — ✓ \U0001F600 end\n\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		raw := []byte(full)
		// Write one byte at a time: every multi-byte rune is split.
		for i := range raw {
			_, _ = w.Write(raw[i : i+1])
			fl.Flush()
		}
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := newFlushRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))

	if rec.Body.String() != full {
		t.Errorf("streamed body was altered\n got: %q\nwant: %q", rec.Body.String(), full)
	}
}

// A non-streaming response must still be copied faithfully.
func TestNonStreamingResponseIsCopiedVerbatim(t *testing.T) {
	payload := strings.Repeat("payload ", 5000)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))

	if rec.Body.String() != payload {
		t.Error("a non-streaming response body was altered")
	}
}
