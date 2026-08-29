package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The counter behind wrap's zero-request warning (bead hr-sw9) must count
// requests that ARRIVED, not requests that completed a hop. An oversize body
// is rejected with 413 by readBody and never reaches s.fwd.ServeHTTP, so it is
// the shape that tells the two definitions apart: move the increment below the
// hop and this test fails.
//
// It matters because the warning it feeds says "the agent sent ZERO requests
// through the proxy", and an agent whose every request was rejected did send
// them. Telling the user to check their routing would send them hunting for a
// bypass that is not there.
func TestRequestCountCountsArrivalsNotCompletions(t *testing.T) {
	srv := testServer(t, nil, "https://upstream.invalid")
	if got := srv.RequestCount(); got != 0 {
		t.Fatalf("a fresh server has seen %d requests, want 0", got)
	}

	rec := httptest.NewRecorder()
	oversize := strings.Repeat("x", (1<<20)+1) // MaxBodyBytes in testServer is 1 MiB.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(oversize))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; the fixture no longer reaches the early return", rec.Code)
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("RequestCount = %d, want 1: a rejected request still arrived", got)
	}
}
