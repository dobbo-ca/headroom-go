package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// toolLog is one tool_result's text: a failure line plus repetitive INFO
// lines, which is the shape the log compressors actually fire on.
//
// Each turn's log is DISTINCT, so every block hashes to its own CCR key. With
// identical blocks a test could not tell "two blocks compressed" from "one
// block compressed twice", and a marker count would prove nothing.
func toolLog(turn int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FAILED: build %d broke\\n", turn)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "INFO 2026-08-25 build=%d worker processed record status=ok\\n", turn)
	}
	return b.String()
}

// compressibleTurn builds a body carrying extraMessages+1 tool_result blocks,
// each with its own log, as an agent loop's Nth turn would.
func compressibleTurn(extraMessages int) []byte {
	msgs := []string{`{"role":"user","content":[{"type":"text","text":"go"}]}`}
	for i := 0; i <= extraMessages; i++ {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs,
			`{"role":"assistant","content":[{"type":"tool_use","id":"`+id+`","name":"Bash","input":{}}]}`,
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+id+`","content":"`+toolLog(i)+`"}]}`)
	}
	return []byte(`{"model":"claude-sonnet-5","messages":[` + strings.Join(msgs, ",") + `]}`)
}

func replayServer(t *testing.T, upstream string, store ccr.Store) *Server {
	t.Helper()
	if store == nil {
		store = newMapStore()
	}
	return New(Deps{
		Config: Config{Upstream: upstream, MaxBodyBytes: 1 << 24, Compress: true,
			RequestTimeout: 5 * time.Second, Replay: true},
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer("claude"),
		Version:   "test",
	})
}

// post sends one turn through the proxy.
func post(t *testing.T, front *httptest.Server, body []byte, headers map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// recorder is an upstream that keeps every body it was handed.
func recorder(seen *[][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*seen = append(*seen, b)
		_, _ = w.Write([]byte(`{"id":"m","type":"message"}`))
	}))
}

// GUARD RAIL 1. A client that declares neither session header must get NO
// replay — a safe no-op, not a guess against an identity that is per-tenant
// and rotates per TCP connection.
func TestReplayIsANoOpWithoutADeclaredSessionID(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	srv := replayServer(t, up.URL, nil)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	// No x-headroom-session-id, no x-claude-code-session-id, no credential.
	post(t, front, compressibleTurn(0), nil)
	post(t, front, compressibleTurn(1), nil)

	if srv.replay.Len() != 0 {
		t.Errorf("replay tracked %d sessions for a client that declared none", srv.replay.Len())
	}
	// Prove the fixture DOES compress, or this test would pass on a body
	// nothing could have replayed anyway.
	if len(seen[0]) >= len(compressibleTurn(0)) {
		t.Fatalf("the fixture did not compress: %d bytes out of %d in",
			len(seen[0]), len(compressibleTurn(0)))
	}
	// Turn 2 re-sends turn one's block. Without replay that older block goes
	// upstream in its ORIGINAL form, so only the newest message carries a
	// marker: exactly one canonical marker, not two.
	if got := bytes.Count(seen[1], []byte("<<ccr:")); got != 1 {
		t.Errorf("turn 2 carried %d canonical markers; without replay only the newest message may be rewritten", got)
	}
}

// The same traffic WITH a declared session id must replay, or the test above
// proves nothing: it would pass on a build where replay is simply broken.
func TestReplayFiresWithADeclaredSessionID(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	srv := replayServer(t, up.URL, nil)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	h := map[string]string{"X-Claude-Code-Session-Id": "fe814859-5860-4fa3-a2dc-7906e146c71a"}
	post(t, front, compressibleTurn(0), h)
	post(t, front, compressibleTurn(1), h)

	if srv.replay.Len() != 1 {
		t.Fatalf("replay tracked %d sessions, want 1", srv.replay.Len())
	}
	if got := bytes.Count(seen[1], []byte("<<ccr:")); got != 2 {
		t.Errorf("turn 2 carried %d canonical markers, want 2: the older block was not replayed", got)
	}
}

// An x-headroom-session-id must work too — it is the arm a non-Claude client
// uses to opt in.
func TestReplayHonoursTheHeadroomSessionHeader(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	srv := replayServer(t, up.URL, nil)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	post(t, front, compressibleTurn(0), map[string]string{"X-Headroom-Session-Id": "my-session"})
	if srv.replay.Len() != 1 {
		t.Errorf("replay tracked %d sessions for a client declaring x-headroom-session-id", srv.replay.Len())
	}
}

// A credential alone must NOT be enough. It identifies a tenant, and an
// interactive client sends the same one for every conversation it has open.
func TestACredentialAloneDoesNotEnableReplay(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	srv := replayServer(t, up.URL, nil)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	post(t, front, compressibleTurn(0), map[string]string{"Authorization": "Bearer sk-ant-oat01-x"})
	post(t, front, compressibleTurn(0), map[string]string{"X-Api-Key": "sk-ant-api-x"})
	if srv.replay.Len() != 0 {
		t.Errorf("replay tracked %d sessions from a credential alone", srv.replay.Len())
	}
}

// blackHoleStore accepts every Put and returns nothing, which is what a full
// disk, a revoked file, or an over-eager eviction looks like from here.
type blackHoleStore struct{ puts int }

func (s *blackHoleStore) Put(string, string)        { s.puts++ }
func (s *blackHoleStore) Get(string) (string, bool) { return "", false }
func (s *blackHoleStore) Len() int                  { return 0 }

// GUARD RAIL 2. A marker whose original the store cannot hand back must never
// go on the wire. With replay on it would sit in the frozen prefix for the
// rest of the session.
func TestUnresolvableMarkersNeverReachTheWire(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	store := &blackHoleStore{}
	srv := replayServer(t, up.URL, store)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	body := compressibleTurn(0)
	post(t, front, body, map[string]string{"X-Claude-Code-Session-Id": "aaaaaaaa-0000-0000-0000-000000000000"})

	// The store WAS written to, so the compressor really ran and this is not
	// a vacuous "nothing happened" pass.
	if store.puts == 0 {
		t.Fatal("nothing was ever put in the store; the fixture never reached the compressor")
	}
	if !bytes.Equal(seen[0], body) {
		t.Errorf("the body was rewritten against a store that resolves nothing: %d bytes out of %d in",
			len(seen[0]), len(body))
	}
	if got := ccr.HashesIn(string(seen[0])); len(got) != 0 {
		t.Errorf("%d unresolvable markers reached the upstream: %v", len(got), got)
	}
}

// The control for the test above: the SAME traffic against a working store
// must produce markers, and every one of them must resolve.
func TestEveryMarkerOnTheWireResolves(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	store := newMapStore()
	srv := replayServer(t, up.URL, store)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	post(t, front, compressibleTurn(0),
		map[string]string{"X-Claude-Code-Session-Id": "bbbbbbbb-0000-0000-0000-000000000000"})

	hashes := ccr.HashesIn(string(seen[0]))
	if len(hashes) == 0 {
		t.Fatal("no marker reached the wire; the fixture stopped compressing")
	}
	for _, h := range hashes {
		if _, ok := store.Get(h); !ok {
			t.Errorf("marker %s on the wire does not resolve", h)
		}
	}
	// A hash that was never stored must NOT resolve, or the check above is
	// vacuous.
	if _, ok := store.Get("ffffffffffffffffffffffff"); ok {
		t.Error("the store resolves a hash it never held; the check proves nothing")
	}
}

// schemeStore makes exactly ONE of the two marker surfaces fail, chosen by key
// SCHEME rather than by call order.
//
// An accepted block carries both: the dispatcher's <<ccr:HASH>> under
// ComputeKey (BLAKE3) and the compressor's inline hash= under ComputeKeyMD5.
// A store that resolves nothing cannot tell the two checks apart, so it cannot
// prove either one is doing work.
type schemeStore struct {
	inner   ccr.Store
	dropMD5 bool
	dropB3  bool
	corrupt bool // resolve the BLAKE3 key, but with the wrong payload
	md5Puts int
	b3Puts  int
}

func (s *schemeStore) Put(hash, payload string) {
	switch hash {
	case ccr.ComputeKeyMD5([]byte(payload)):
		s.md5Puts++
		if s.dropMD5 {
			return
		}
	case ccr.ComputeKey([]byte(payload)):
		s.b3Puts++
		if s.dropB3 {
			return
		}
		if s.corrupt {
			s.inner.Put(hash, payload+" tampered")
			return
		}
	}
	s.inner.Put(hash, payload)
}

func (s *schemeStore) Get(hash string) (string, bool) { return s.inner.Get(hash) }
func (s *schemeStore) Len() int                       { return s.inner.Len() }

// runTurn posts one compressible turn and returns what reached the upstream.
func runTurn(t *testing.T, store ccr.Store) []byte {
	t.Helper()
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	srv := replayServer(t, up.URL, store)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	post(t, front, compressibleTurn(0), map[string]string{
		"X-Claude-Code-Session-Id": "cccccccc-0000-0000-0000-000000000000"})
	return seen[0]
}

// Both marker surfaces must be checked independently. Each case here fails
// only if the check for THAT surface is doing work; a store that resolves
// nothing would pass whichever check survived.
func TestBothMarkerSurfacesAreCheckedIndependently(t *testing.T) {
	body := compressibleTurn(0)

	// Control: the fixture must produce BOTH kinds of write, or dropping one
	// of them proves nothing.
	probe := &schemeStore{inner: newMapStore()}
	if out := runTurn(t, probe); len(out) >= len(body) {
		t.Fatalf("the fixture did not compress: %d bytes out of %d in", len(out), len(body))
	}
	if probe.md5Puts == 0 || probe.b3Puts == 0 {
		t.Fatalf("the fixture wrote %d MD5-keyed and %d BLAKE3-keyed entries; "+
			"both surfaces must be exercised", probe.md5Puts, probe.b3Puts)
	}

	tests := []struct {
		name  string
		store *schemeStore
	}{
		{"the compressor's inline hash= does not resolve", &schemeStore{inner: newMapStore(), dropMD5: true}},
		{"the canonical <<ccr:HASH>> does not resolve", &schemeStore{inner: newMapStore(), dropB3: true}},
		{"the canonical hash resolves to the wrong bytes", &schemeStore{inner: newMapStore(), corrupt: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runTurn(t, tt.store)
			if !bytes.Equal(out, body) {
				t.Errorf("the body was rewritten anyway: %d bytes out of %d in", len(out), len(body))
			}
			if got := ccr.HashesIn(string(out)); len(got) != 0 {
				t.Errorf("%d markers reached the upstream that the model could not dereference: %v", len(got), got)
			}
		})
	}
}

// losingStore models the case replay actually has to survive: the entries a
// past turn wrote have expired or been evicted, and new writes are not landing
// either — a full disk, or a store whose TTL has passed.
type losingStore struct {
	m       map[string]string
	failing bool
}

func newLosingStore() *losingStore { return &losingStore{m: map[string]string{}} }

func (s *losingStore) Put(hash, payload string) {
	if s.failing {
		return
	}
	s.m[hash] = payload
}

func (s *losingStore) Get(hash string) (string, bool) {
	v, ok := s.m[hash]
	return v, ok
}

func (s *losingStore) Len() int { return len(s.m) }

func (s *losingStore) expireAll() { s.m = map[string]string{} }

// The REPLAY pass has its own store check, and it is the one that matters
// most: a replayed marker sits inside the cache-frozen prefix, so an
// unresolvable one stays in front of the model for the rest of the session.
//
// Turn 1 compresses and stores. The store then loses everything and stops
// accepting writes. Turn 2 must send the ORIGINAL bytes rather than replay a
// marker nothing can dereference, and must DROP the dead entry: the block sits
// below the frozen floor, so it can never be compressed again, and an entry
// that can only fail would otherwise cost a store read every turn for the rest
// of the session. The staleness sweep cannot collect it, because replay looks
// it up every turn and that is exactly what marks an entry live.
func TestReplayDropsAMarkerTheStoreCanNoLongerResolve(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	store := newLosingStore()
	srv := replayServer(t, up.URL, store)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	const sessionID = "dddddddd-0000-0000-0000-000000000000"
	h := map[string]string{"X-Claude-Code-Session-Id": sessionID}

	post(t, front, compressibleTurn(0), h)
	if len(ccr.HashesIn(string(seen[0]))) == 0 {
		t.Fatal("turn 1 did not compress; the rest of this test would prove nothing")
	}

	// The store loses its entries and stops accepting writes.
	store.expireAll()
	store.failing = true

	turn2 := compressibleTurn(1)
	post(t, front, turn2, h)
	if !bytes.Equal(seen[1], turn2) {
		t.Errorf("turn 2 was rewritten against a store that resolves nothing: %d bytes out of %d in",
			len(seen[1]), len(turn2))
	}
	if got := ccr.HashesIn(string(seen[1])); len(got) != 0 {
		t.Errorf("turn 2 put %d unresolvable markers on the wire: %v", len(got), got)
	}

	// The dead entry must be gone from the replay map, not merely skipped.
	hdr := http.Header{}
	hdr.Set("X-Claude-Code-Session-Id", sessionID)
	key, declared := cachestab.DeclaredSessionKey(hdr)
	if !declared {
		t.Fatal("the test header did not declare a session")
	}
	if n := srv.replay.Begin(key, turn2).EntryCount(); n != 0 {
		t.Errorf("%d dead replay entries survived; each costs a store read every turn "+
			"and the sweep can never collect them", n)
	}
}

// Replay re-stores the original on every hit. The CCR TTL is five minutes and
// a session runs far longer, so without that refresh a marker still on the
// wire outlives the entry it dereferences — and the guard then correctly
// refuses to replay it, which costs the saving.
//
// This is why the TTL itself does not need to change: the refresh keeps alive
// exactly the working set that is on the wire, and nothing else.
func TestReplayRefreshesTheStoreEntryBeforeItExpires(t *testing.T) {
	var seen [][]byte
	up := recorder(&seen)
	defer up.Close()

	// A real backend with a TTL shorter than the gap between turns.
	store, err := ccr.FromConfig(ccr.BackendConfig{
		Kind: ccr.InMemory, Capacity: 100, TTLSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := replayServer(t, up.URL, store)
	front := httptest.NewServer(srv.Handler())
	defer front.Close()

	h := map[string]string{"X-Claude-Code-Session-Id": "eeeeeeee-0000-0000-0000-000000000000"}

	post(t, front, compressibleTurn(0), h)
	first := ccr.HashesIn(string(seen[0]))
	if len(first) == 0 {
		t.Fatal("turn 1 did not compress")
	}

	// Control: without a refresh the entry would be gone by now.
	time.Sleep(1200 * time.Millisecond)

	post(t, front, compressibleTurn(1), h)
	if got := bytes.Count(seen[1], []byte("<<ccr:")); got != 2 {
		t.Fatalf("turn 2 carried %d canonical markers, want 2: the expired entry was not refreshed, "+
			"so the older block could not be replayed", got)
	}
	onWire := map[string]bool{}
	for _, hash := range ccr.HashesIn(string(seen[1])) {
		onWire[hash] = true
		if _, ok := store.Get(hash); !ok {
			t.Errorf("turn 2 marker %s does not resolve", hash)
		}
	}
	// And no more than that. Refreshing an entry no marker names would leave
	// an orphan in the store, which is the failure the staging store exists
	// to prevent on the fresh path.
	if store.Len() != len(onWire) {
		t.Errorf("the store holds %d entries for %d markers on the wire; replay wrote an orphan",
			store.Len(), len(onWire))
	}
}
