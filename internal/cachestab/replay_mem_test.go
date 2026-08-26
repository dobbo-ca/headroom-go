package cachestab

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// A machine running for days is the case replay was never tested against.
// ReplayState holds 256 sessions with FIFO eviction and, before this was
// measured, unbounded entries per session. This test reports what a heavy day
// actually costs, so the bound is decided from a number rather than a feeling.
//
// It asserts only the ceiling that matters. The printed figures are the
// evidence; run with -v to read them.
func TestReplayStateMemoryUnderADayOfUse(t *testing.T) {
	// A compressed block as it actually looks on the wire: the log
	// compressors take a 16 KB tool_result down to a few hundred bytes,
	// and 1 KB is the pessimistic end of that.
	const compressed = 1 << 10
	// Every payload must be DISTINCT. A shared string makes every entry
	// point at one backing array, and the heap figure then reports the map
	// overhead only — a vacuous measurement that reads as "replay is free".
	payload := func(n int) string {
		p := fmt.Sprintf("%d:", n)
		return p + strings.Repeat("x", compressed-len(p))
	}

	cases := []struct{ sessions, blocksPerSession int }{
		{1, 1000},   // one long session
		{50, 200},   // a working day across many sessions
		{256, 1000}, // every slot full and every session heavy
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dx%d", c.sessions, c.blocksPerSession), func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			s := NewReplayState(DefaultReplayCapacity)
			for i := 0; i < c.sessions; i++ {
				key := fmt.Sprintf("claude:%016x", i)
				h := s.Begin(key, []byte(`{"messages":[]}`))
				for b := 0; b < c.blocksPerSession; b++ {
					n := i*c.blocksPerSession + b
					h.Record(fmt.Sprintf("%024x", n), payload(n))
				}
			}

			runtime.GC()
			runtime.ReadMemStats(&after)
			used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			payloadBytes := int64(c.sessions) * int64(c.blocksPerSession) * compressed
			t.Logf("%d sessions x %d blocks: heap %.1f MB (payload alone %.1f MB), %d sessions retained",
				c.sessions, c.blocksPerSession,
				float64(used)/(1<<20), float64(payloadBytes)/(1<<20), s.Len())

			// CONTROL: the payloads are distinct, so the heap must be at
			// least the payload bytes. If it is not, the measurement is not
			// seeing the entries at all.
			if used < payloadBytes {
				t.Fatalf("heap grew %d bytes for %d bytes of distinct payload; the measurement is vacuous",
					used, payloadBytes)
			}
			runtime.KeepAlive(s)
		})
	}
}

// The sweep is the bound. With every session holding 1000 blocks but only 20
// still in its conversation, memory must fall to the LIVE working set rather
// than the whole day's history.
//
// The measurement above says the unswept worst case is 276 MB. This one says
// what it costs once the client stops re-sending the old blocks.
func TestReplaySweepBoundsAMachineRunningForDays(t *testing.T) {
	const (
		compressed = 1 << 10
		sessions   = 256
		history    = 1000
		live       = 20
	)
	payload := func(n int) string {
		p := fmt.Sprintf("%d:", n)
		return p + strings.Repeat("x", compressed-len(p))
	}
	body := []byte(`{"messages":[]}`)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	s := NewReplayState(DefaultReplayCapacity)
	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("claude:%016x", i)
		h := s.Begin(key, body)
		for b := 0; b < history; b++ {
			h.Record(fmt.Sprintf("%024x", i*history+b), payload(i*history+b))
		}
		// The conversation moves on: only the newest `live` blocks are still
		// re-sent, for longer than the stale window.
		for turn := 0; turn <= ReplayStaleTurns; turn++ {
			h = s.Begin(key, body)
			for b := history - live; b < history; b++ {
				if _, ok := h.Lookup(fmt.Sprintf("%024x", i*history+b)); !ok {
					t.Fatalf("session %d turn %d: a live block was swept", i, turn)
				}
			}
		}
		if got := h.EntryCount(); got != live {
			t.Fatalf("session %d holds %d entries, want the %d live ones", i, got, live)
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d sessions x %d blocks of history, %d live each: heap %.1f MB "+
		"(unswept this would be %.1f MB of payload alone)",
		sessions, history, live, float64(used)/(1<<20),
		float64(sessions)*float64(history)*compressed/(1<<20))

	// The ceiling that matters: the live set is 5 MB of payload, so anything
	// near the 250 MB unswept figure means the sweep is not running.
	const ceiling = 64 << 20
	if used > ceiling {
		t.Errorf("heap %.1f MB exceeds the %d MB ceiling; the sweep is not bounding a long-running proxy",
			float64(used)/(1<<20), ceiling>>20)
	}
	runtime.KeepAlive(s)
}
