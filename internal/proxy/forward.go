package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/livezone"
	"github.com/dobbo-ca/headroom-go/internal/policy"
)

// anthropicMessagesPath is the only shape the live-zone dispatcher handles.
// OpenAI chat/responses dispatchers are a follow-up, so those paths forward
// unchanged.
const anthropicMessagesPath = "/v1/messages"

// internalHeaderPrefix marks headroom's own headers.
const internalHeaderPrefix = "x-headroom-"

// handleForward is the single funnel every non-headroom route goes through.
// It only chooses the REQUEST body and reports what it did; the hop itself is
// s.fwd. The RESPONSE is never compressed or rewritten (spec 9 risk 4).
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(w, r)
	if err != nil {
		// readBody already wrote the status.
		return
	}

	sent, result := s.maybeCompress(r, body)
	if result != nil && result.Applied {
		// Describe our own work to the client. This direction never crosses
		// an upstream boundary.
		dst := w.Header()
		dst.Set("X-Headroom-Tokens-Before", strconv.Itoa(result.TokensBefore))
		dst.Set("X-Headroom-Tokens-After", strconv.Itoa(result.TokensAfter))
		dst.Set("X-Headroom-Bytes-Saved", strconv.Itoa(len(body)-len(sent)))
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.deps.Config.RequestTimeout)
	defer cancel()

	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(sent))
	r.ContentLength = int64(len(sent))
	s.fwd.ServeHTTP(w, r)
}

// readBody buffers the request body, enforcing the size cap. On an oversize
// body it writes 413 and returns an error.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var src io.Reader = r.Body
	if max := s.deps.Config.MaxBodyBytes; max > 0 {
		src = http.MaxBytesReader(w, r.Body, max)
	}
	body, err := io.ReadAll(src)
	if err != nil {
		http.Error(w, "headroom: request body too large", http.StatusRequestEntityTooLarge)
		return nil, err
	}
	return body, nil
}

// maybeCompress runs the live-zone dispatcher when this request qualifies.
// It returns the bytes to send upstream and, when compression ran, the
// dispatcher result. The returned bytes are ALWAYS safe to forward: on every
// bail-out path Dispatch hands back the input slice.
func (s *Server) maybeCompress(r *http.Request, body []byte) ([]byte, *livezone.Result) {
	if !s.deps.Config.Compress || r.Method != http.MethodPost || len(body) == 0 {
		return body, nil
	}
	if !strings.HasSuffix(r.URL.Path, anthropicMessagesPath) {
		return body, nil
	}

	mode := policy.ClassifyHeader(r.Header)
	res := livezone.Dispatch(body, livezone.Options{
		Policy:      policy.ForMode(mode),
		Router:      s.deps.Router,
		Store:       s.deps.Store,
		Tokenizer:   s.deps.Tokenizer,
		FrozenCount: -1,
	})
	slog.Debug("live-zone dispatch",
		"path", r.URL.Path, "auth_mode", mode.String(), "applied", res.Applied,
		"reason", string(res.Reason), "bytes_in", len(body), "bytes_out", len(res.Body))
	return res.Body, &res
}
