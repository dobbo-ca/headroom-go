package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// Deps are the collaborators the proxy needs.
type Deps struct {
	Config    Config
	Store     ccr.Store
	Router    *router.Router
	Tokenizer tokenizer.Tokenizer
	Version   string
}

// Server is the headroom proxy.
type Server struct {
	deps   Deps
	client *http.Client
	fwd    *httputil.ReverseProxy
	// drift holds the per-session cache-prefix baselines. Observation only:
	// it never reaches the bytes forwarded upstream.
	drift *cachestab.DriftState
}

// New builds a Server. The HTTP client deliberately has NO client-wide
// Timeout: that would cut long SSE streams. Each request carries its own
// context deadline instead.
//
// The hop itself is httputil.ReverseProxy, which already strips hop-by-hop
// and Connection-listed headers in both directions, rewrites Host and
// Content-Length, appends X-Forwarded-For, and flushes text/event-stream
// responses as they arrive. It has no ModifyResponse, so the RESPONSE is
// never rewritten (spec 9 risk 4).
func New(d Deps) *Server {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: d.Config.DialTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	// Load has already validated this URL; a Deps built by hand with an
	// unparseable upstream is a programming error.
	target, _ := url.Parse(d.Config.Upstream)

	return &Server{
		deps:  d,
		drift: cachestab.NewDriftState(cachestab.DefaultDriftCapacity),
		client: &http.Client{
			Transport: transport,
			// Never follow redirects: hand the 3xx back to the client so it
			// keeps control of where its credentials go.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		fwd: &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				// Rewrite strips inbound X-Forwarded-*; put the client's
				// chain back so SetXForwarded appends to it.
				pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
				pr.SetXForwarded()
				// headroom's own headers describe the proxy's work to the
				// client and must never cross the upstream boundary.
				for name := range pr.Out.Header {
					if strings.HasPrefix(strings.ToLower(name), internalHeaderPrefix) {
						delete(pr.Out.Header, name)
					}
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				slog.Warn("upstream request failed", "path", r.URL.Path, "error", err)
				http.Error(w, "headroom: upstream request failed: "+err.Error(), http.StatusBadGateway)
			},
		},
	}
}

// Handler returns the route table. Headroom's own routes are registered
// explicitly; everything else is forwarded.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /healthz/upstream", s.handleHealthzUpstream)
	mux.HandleFunc("POST /v1/retrieve", s.handleRetrieve)
	mux.HandleFunc("/", s.handleForward)
	return mux
}

// ListenAndServe runs the server until ctx is cancelled, then drains.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.deps.Config.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
