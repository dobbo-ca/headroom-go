package proxy

import (
	"errors"
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
		// A malformed parameter must not turn streaming off: buffering an
		// SSE stream would stall live token rendering.
		{"text/event-stream; charset", true},
		{"application/json; charset", false},
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

// failWriter fails every Write, standing in for a client that hung up.
type failWriter struct{ header http.Header }

func (f *failWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *failWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failWriter) WriteHeader(int)           {}
func (f *failWriter) Flush()                    {}

// endlessReader never ends; it counts how many reads the proxy performed.
type endlessReader struct{ reads int }

func (e *endlessReader) Read(p []byte) (int, error) {
	e.reads++
	if e.reads > 1000 {
		return 0, errors.New("runaway: copyResponse kept reading after a write error")
	}
	p[0] = 'x'
	return 1, nil
}
func (e *endlessReader) Close() error { return nil }

// When the client goes away mid-stream, copyResponse must stop; otherwise it
// drains the whole upstream stream into a dead socket.
func TestStreamingStopsWhenClientWriteFails(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	body := &endlessReader{}
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   body,
	}

	done := make(chan struct{})
	go func() {
		srv.copyResponse(&failWriter{}, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("copyResponse did not return after the client write failed")
	}
	if body.reads != 1 {
		t.Errorf("upstream was read %d times; must stop at the first failed write", body.reads)
	}
}
