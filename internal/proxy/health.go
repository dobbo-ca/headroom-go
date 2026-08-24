package proxy

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.deps.Version,
	})
}

// handleHealthzUpstream probes the configured upstream. A short deadline
// keeps a wedged upstream from wedging the health check.
func (s *Server) handleHealthzUpstream(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.deps.Config.Upstream, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "unreachable", "error": err.Error()})
		return
	}
	defer resp.Body.Close()

	// Any HTTP answer means the upstream is reachable; a 404 on HEAD / is a
	// perfectly healthy API gateway.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"upstream":        s.deps.Config.Upstream,
		"upstream_status": resp.StatusCode,
	})
}
