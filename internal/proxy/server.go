package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

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
}

// New builds a Server. The HTTP client deliberately has NO client-wide
// Timeout: that would cut long SSE streams. Each request carries its own
// context deadline instead.
func New(d Deps) *Server {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: d.Config.DialTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Server{
		deps: d,
		client: &http.Client{
			Transport: transport,
			// Never follow redirects: hand the 3xx back to the client so it
			// keeps control of where its credentials go.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
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

// handleForward is filled in by Task 4.
func (s *Server) handleForward(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "forwarding not implemented", http.StatusNotImplemented)
}
