package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
)

// The detectors are observation only. Wiring them in must not change one byte
// of what reaches the upstream, on a body that fires BOTH of them.
func TestCacheStabilityObservationNeverAltersForwardedBytes(t *testing.T) {
	// Both detectable shapes sit where the scanner actually looks: system and
	// messages. Tool definitions are deliberately not scanned.
	body := []byte(`{"model":"m","system":"built 2026-08-25T09:37:11Z",` +
		`"tools":[{"name":"t","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi",` +
		`"trace_id":"91827","cache_control":{"type":"ephemeral"}}]}]}`)

	// Prove the fixture reaches the detector, or this test proves nothing.
	if fs := cachestab.DetectVolatile(body); len(fs) < 2 {
		t.Fatalf("fixture produced %d volatile findings, want at least a timestamp and an id field", len(fs))
	}

	var upstream []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if !bytes.Equal(upstream, body) {
		t.Errorf("the observed body was altered: upstream got %d bytes, client sent %d",
			len(upstream), len(body))
	}
}

// The drift detector must actually be wired: a session's baseline has to be
// recorded, and a rewritten prefix has to be seen as drift.
func TestProxyRecordsDriftAcrossTurns(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
	defer up.Close()

	srv := testServer(t, nil, up.URL)
	if srv.drift == nil {
		t.Fatal("the server has no drift state; the detector is not wired")
	}

	post := func(system string) {
		t.Helper()
		body := `{"model":"m","system":"` + system + `","tools":[],"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"a"}]}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("X-Claude-Code-Session-Id", "session-under-test")
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	post("original")
	if n := srv.drift.Len(); n != 1 {
		t.Fatalf("drift state holds %d sessions after one turn, want 1", n)
	}

	// A second turn on the same session with a rewritten system prompt is
	// drift. Observe it directly so the assertion names the dimension.
	key := cachestab.SessionKey(
		http.Header{"X-Claude-Code-Session-Id": []string{"session-under-test"}}, "",
		[]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`))
	changed := cachestab.ComputeStructuralHash([]byte(
		`{"model":"m","system":"REWRITTEN","tools":[],"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"a"}]}]}`))
	obs := srv.drift.Observe(key, changed)
	if obs.FirstRequest {
		t.Fatal("the proxy did not record a baseline under the session key it derives")
	}
	if !obs.Drifted() || obs.Dims[0] != "system" {
		t.Errorf("second turn reported drifted=%v dims=%v, want [system]", obs.Drifted(), obs.Dims)
	}
}
