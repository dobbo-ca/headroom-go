package proxy

import (
	"io"
	"mime"
	"net/http"
	"strings"
)

// streamBufSize is the read chunk for a streaming response. Big enough that
// a large event is one write, small enough that a token arrives promptly.
const streamBufSize = 32 * 1024

// isStreaming reports whether the response is server-sent events.
func isStreaming(h http.Header) bool {
	ct := h.Get("Content-Type")
	if ct == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		// Fall back to a prefix check rather than deciding it is not a
		// stream: a malformed parameter must not turn streaming off.
		mediaType = strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

// copyResponse streams the upstream body to the client.
//
// The response is NEVER compressed or rewritten (spec 9 risk 4). For an SSE
// stream we flush after every chunk so tokens render as they arrive; there is
// deliberately no framing and no UTF-8 decoding, so a multi-byte rune split
// across two reads is simply forwarded as two writes.
func (s *Server) copyResponse(w http.ResponseWriter, resp *http.Response) {
	if !isStreaming(resp.Header) {
		_, _ = io.Copy(w, resp.Body)
		return
	}

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, streamBufSize)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
