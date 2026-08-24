package proxy

import (
	"encoding/json"
	"net/http"
	"regexp"
)

// hashRe matches a ccr.ComputeKey output: 24 lowercase hex characters. Same
// shape internal/mcp validates, so the two ends agree.
var hashRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// handleRetrieve serves the CCR lookup that internal/mcp's headroom_retrieve
// falls back to. It is headroom's own route and never reaches the upstream.
//
// The wire contract is fixed by internal/mcp/retrieve.go: request
// {"hash":"<24 lowercase hex>"}, 200 {"content":"..."} on a hit, any non-200
// treated as a miss by that client.
func (s *Server) handleRetrieve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"found": false, "error": "body must be JSON with a hash field",
		})
		return
	}
	if !hashRe.MatchString(req.Hash) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"found": false, "error": "hash must be 24 lowercase hex characters",
		})
		return
	}

	payload, ok := s.deps.Store.Get(req.Hash)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"found": false, "hash": req.Hash})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found": true, "hash": req.Hash, "content": payload,
	})
}
