package cachestab

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// bodyOfMessages builds a body carrying n messages, which is all Begin reads.
func bodyOfMessages(n int) []byte {
	out := `{"messages":[`
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += `{"role":"user","content":"m` + strconv.Itoa(i) + `"}`
	}
	return []byte(out + `]}`)
}

// The floor is the whole point of the type: turn N+1 may only compress fresh
// what turn N did not already send.
func TestFloorIsThePreviousTurnsMessageCount(t *testing.T) {
	s := NewReplayState(8)

	first := s.Begin("k", bodyOfMessages(3))
	if !first.FirstTurn() {
		t.Error("a session's first Begin must report FirstTurn")
	}
	if got := first.Floor(); got != 0 {
		t.Errorf("first turn Floor() = %d, want 0", got)
	}

	second := s.Begin("k", bodyOfMessages(5))
	if second.FirstTurn() {
		t.Error("the second Begin on a key must not report FirstTurn")
	}
	if got := second.Floor(); got != 3 {
		t.Errorf("second turn Floor() = %d, want 3 (what turn 1 sent)", got)
	}

	if got := s.Begin("k", bodyOfMessages(9)).Floor(); got != 5 {
		t.Errorf("third turn Floor() = %d, want 5 (what turn 2 sent)", got)
	}
}

// Two conversations must not share a floor or a block map, or one session's
// compression would be replayed into another's prefix.
func TestSessionsAreIsolated(t *testing.T) {
	s := NewReplayState(8)
	s.Begin("a", bodyOfMessages(4)).Record("h", "compressed-a")

	b := s.Begin("b", bodyOfMessages(1))
	if !b.FirstTurn() {
		t.Error("a different key must be a first turn")
	}
	if got := b.Floor(); got != 0 {
		t.Errorf("session b Floor() = %d, want 0; it inherited session a's floor", got)
	}
	if v, ok := b.Lookup("h"); ok {
		t.Errorf("session b resolved session a's block to %q", v)
	}

	if v, ok := s.Begin("a", bodyOfMessages(6)).Lookup("h"); !ok || v != "compressed-a" {
		t.Errorf("session a lost its own block: got %q, ok=%v", v, ok)
	}
}

// A proxy runs for days. Without a bound the map is a leak.
func TestReplayStateEvictsOldestSession(t *testing.T) {
	s := NewReplayState(2)
	s.Begin("a", bodyOfMessages(2)).Record("h", "x")
	s.Begin("b", bodyOfMessages(2))
	s.Begin("c", bodyOfMessages(2))

	if got := s.Len(); got != 2 {
		t.Errorf("Len() = %d, want the capacity 2", got)
	}
	revived := s.Begin("a", bodyOfMessages(2))
	if !revived.FirstTurn() {
		t.Error("session a survived eviction; the capacity bound does not hold")
	}
	if _, ok := revived.Lookup("h"); ok {
		t.Error("evicted session a still resolves its blocks")
	}
	// An evicted session degrades to a first turn, which is safe: it stops
	// replaying rather than replaying something stale.
	if got := revived.Floor(); got != 0 {
		t.Errorf("revived Floor() = %d, want 0", got)
	}
}

// A zero or negative capacity must select the default rather than a state that
// evicts every session on arrival.
func TestNonPositiveCapacitySelectsTheDefault(t *testing.T) {
	for _, c := range []int{0, -1} {
		s := NewReplayState(c)
		for i := 0; i < DefaultReplayCapacity; i++ {
			s.Begin("k"+strconv.Itoa(i), bodyOfMessages(1))
		}
		if got := s.Len(); got != DefaultReplayCapacity {
			t.Errorf("capacity %d held %d sessions, want %d", c, got, DefaultReplayCapacity)
		}
	}
}

// Begin reads the count off the body it is handed. A malformed or empty body
// must floor at 0 rather than at some stale value.
func TestBeginCountsMessagesInTheBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want int
	}{
		{"seven messages", bodyOfMessages(7), 7},
		{"no messages field", []byte(`{"model":"claude-sonnet-5"}`), 0},
		{"not json", []byte("garbage"), 0},
		{"nil", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewReplayState(4)
			s.Begin("k", tc.body)
			if got := s.Begin("k", bodyOfMessages(1)).Floor(); got != tc.want {
				t.Errorf("Floor() after %s = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// The proxy serves requests concurrently, so the state must survive -race.
func TestReplayStateIsConcurrencySafe(t *testing.T) {
	s := NewReplayState(16)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + strconv.Itoa(i%4)
			h := s.Begin(key, bodyOfMessages(i%5))
			h.Record(fmt.Sprintf("h%d", i), "compressed")
			h.Lookup("h0")
			_ = h.Floor()
		}(i)
	}
	wg.Wait()
	if got := s.Len(); got != 4 {
		t.Errorf("Len() = %d, want 4 distinct sessions", got)
	}
}
