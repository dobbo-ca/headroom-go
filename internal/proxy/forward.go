package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/livezone"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/tidwall/gjson"
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
	sessionKey := cachestab.SessionKey(r.Header, r.RemoteAddr, body)
	drift := s.observeCacheStability(sessionKey, body)

	// The real frozen floor is what the PREVIOUS turn of this session sent,
	// not where the client put its newest cache_control marker. That marker
	// is a cache WRITE instruction for bytes the provider has never seen;
	// reading it as a read guarantee freezes the whole conversation, which
	// is why headroom saves nothing on an agent client today.
	//
	// A first turn floors at 0, and that is safe rather than reckless: the
	// fresh pass only ever touches the LATEST user message, which is the
	// one the client just appended and the provider has never seen. Without
	// this, a block is never compressed on the turn it first appears, so it
	// never enters the replay map and the feature does nothing at all.
	frozen, replay := -1, s.sessionReplay(r.Header, body)
	if replay != nil {
		frozen = replay.Floor()
	}
	res := livezone.Dispatch(body, livezone.Options{
		Policy:      policy.ForMode(mode),
		Router:      s.deps.Router,
		Store:       s.deps.Store,
		Tokenizer:   s.deps.Tokenizer,
		FrozenCount: frozen,
		Replay:      replay,
	})
	slog.Debug("live-zone dispatch",
		"path", r.URL.Path, "auth_mode", mode.String(), "applied", res.Applied,
		"reason", string(res.Reason), "bytes_in", len(body), "bytes_out", len(res.Body),
		"frozen_count", res.FrozenCount, "replay", replay != nil,
		"replay_first_turn", replay != nil && replay.FirstTurn())

	s.record(sessionKey, body, drift, res)
	return res.Body, &res
}

// record appends this turn to the ledger. It runs AFTER the dispatcher and
// nothing reads it back, so the timestamp it stamps cannot reach the bytes
// forwarded upstream and determinism (I4) still holds.
func (s *Server) record(sessionKey string, body []byte, drift []string, res livezone.Result) {
	if s.ledger == nil {
		return
	}
	var strategies []string
	seen := map[string]bool{}
	replayed := 0
	for _, b := range res.Blocks {
		if b.Action == "replayed" {
			replayed++
		}
		if b.Strategy != "" && !seen[b.Strategy] {
			seen[b.Strategy] = true
			strategies = append(strategies, b.Strategy)
		}
	}
	sort.Strings(strategies)

	s.ledger.Append(ledger.Entry{
		Session:      cachestab.Digest(sessionKey),
		Model:        gjson.GetBytes(body, "model").String(),
		Messages:     int(gjson.GetBytes(body, "messages.#").Int()),
		BytesIn:      len(body),
		BytesOut:     len(res.Body),
		TokensBefore: res.TokensBefore,
		TokensAfter:  res.TokensAfter,
		Reason:       string(res.Reason),
		Strategies:   strategies,
		Replayed:     replayed,
		Drift:        drift,
	})
}

// sessionReplay opens this turn's replay handle, or returns nil when replay
// is off or the client declared no session. It is the ONLY path by which
// per-session state reaches the bytes forwarded upstream.
//
// The identity here is deliberately NOT the one drift detection uses. Drift
// only logs, so an inferred identity costs a wrong log line; replay rewrites
// bytes, so an inferred identity could serve one conversation the compressed
// blocks of another. A client that declares no session gets no replay, and
// says so once rather than guessing for a whole session.
func (s *Server) sessionReplay(h http.Header, body []byte) *cachestab.SessionReplay {
	if s.replay == nil {
		return nil
	}
	key, declared := cachestab.DeclaredSessionKey(h)
	if !declared {
		slog.Warn("replay is off for this request: the client declared no session id",
			"event", "replay_no_session_id",
			"want_header", "x-headroom-session-id or x-claude-code-session-id")
		return nil
	}
	return s.replay.Begin(key, body)
}

// observeCacheStability runs the two cache-stabilization detectors over the
// buffered body. Both are READ-ONLY and log only: they take the bytes and
// return findings, so no path here can reach the bytes forwarded upstream.
//
// This runs before the dispatcher deliberately. The detectors describe what
// the CLIENT sent, which is what the customer can act on; describing our own
// output would just report our own compression as drift.
func (s *Server) observeCacheStability(sessionKey string, body []byte) []string {
	for _, f := range cachestab.DetectVolatile(body) {
		slog.Warn("volatile content in the cached prefix will bust prompt-cache hits",
			"event", "volatile_content_detected",
			"kind", string(f.Kind), "location", f.Location, "sample", f.Sample)
	}

	if s.drift == nil {
		return nil
	}
	obs := s.drift.Observe(sessionKey, cachestab.ComputeStructuralHash(body))
	switch {
	case obs.FirstRequest:
		slog.Debug("cache drift baseline recorded",
			"event", "cache_drift_first_request", "session", obs.SessionDigest)
	case obs.Drifted():
		slog.Warn("the cached prefix changed between turns; the provider cache was re-written",
			"event", "cache_drift_observed",
			"session", obs.SessionDigest, "dimensions", strings.Join(obs.Dims, ","))
	default:
		slog.Debug("cached prefix stable",
			"event", "cache_prefix_stable", "session", obs.SessionDigest)
	}
	if obs.Drifted() {
		return obs.Dims
	}
	return nil
}
