package cachestab

import (
	"sync"

	"github.com/tidwall/gjson"
)

// DefaultReplayCapacity bounds how many sessions carry replay state. Each
// session holds only the COMPRESSED text of the blocks headroom rewrote —
// bytes that by construction are smaller than what went on the wire — so the
// bound is on session count rather than on total size.
const DefaultReplayCapacity = 256

// ReplayState remembers, per session, which blocks headroom has already
// compressed and what it replaced them with.
//
// Anthropic keys its prompt cache on the prefix BYTES. A client re-sends the
// whole conversation every turn and has no idea headroom compressed anything,
// so a block arrives in its ORIGINAL form on every later turn. Compressing it
// once and then leaving it alone rewrites the cached prefix and costs a cache
// miss EVERY turn — strictly worse than never compressing it at all.
//
// Replay closes that hole: any block this session has compressed before is
// replaced with the same bytes again, wherever it now sits. The prefix is then
// byte-stable across turns AND smaller.
//
// Eviction is FIFO, matching [DriftState]; the same reasoning applies.
//
// Within a session, entries are swept by staleness rather than capped by
// count. See [ReplayStaleTurns] for why, and TestReplayStateMemoryUnderADayOf-
// Use for the measurement that decided it.
type ReplayState struct {
	mu       sync.Mutex
	capacity int
	sessions map[string]*replaySession
	order    []string
}

// ReplayStaleTurns is how many consecutive turns an entry may go unseen before
// it is swept.
//
// A client re-sends every message it still holds on every turn, so a block
// that is still in the conversation is looked up EVERY turn. An entry nobody
// asked about for three turns therefore belongs to a block that has scrolled
// out — compacted away, or dropped by a rolling window — and it will never be
// looked up again. Keeping it is pure leak.
//
// The bound is on staleness rather than on count deliberately: evicting a LIVE
// entry guarantees a prompt-cache miss on the next turn, which is the exact
// cost replay exists to avoid. Sweeping a dead one costs nothing.
//
// Measured before choosing this (see replay_mem_test.go, distinct payloads):
// a working day of 50 sessions x 200 blocks holds 10.4 MB, but every slot full
// and every session heavy — 256 x 1000 — holds 276 MB. Unbounded entries per
// session was the case a machine running for days would have found.
const ReplayStaleTurns = 3

// replayEntry is one block's compressed text plus the turn it was last seen.
type replayEntry struct {
	text string
	seen int
}

// replaySession is one conversation's replay state.
type replaySession struct {
	// turn counts this session's requests, and is the clock the staleness
	// sweep runs on.
	turn int
	// sentCount is how many messages this session's PREVIOUS turn carried.
	// It is the real frozen floor: every message below it has already gone
	// to the provider and may sit in the provider's cache, so only an
	// index at or above it is safe to compress fresh.
	//
	// This is not the same as the cache_control floor. A client marks its
	// NEWEST message, and that marker is a cache WRITE instruction for
	// bytes never sent before, not a read guarantee — which is why
	// cachecontrol.ComputeFrozenCount freezes the whole conversation for
	// Claude Code and headroom saves nothing.
	sentCount int
	// blocks maps ccr.ComputeKey(original) to the compressed text that was
	// sent in its place.
	blocks map[string]replayEntry
}

// sweep drops entries no turn within ReplayStaleTurns has asked about.
// Called under the parent's lock.
func (s *replaySession) sweep() {
	cutoff := s.turn - ReplayStaleTurns
	if cutoff <= 0 {
		return
	}
	for k, e := range s.blocks {
		if e.seen < cutoff {
			delete(s.blocks, k)
		}
	}
}

// NewReplayState builds a ReplayState. A capacity of zero or less selects
// DefaultReplayCapacity.
func NewReplayState(capacity int) *ReplayState {
	if capacity <= 0 {
		capacity = DefaultReplayCapacity
	}
	return &ReplayState{capacity: capacity, sessions: make(map[string]*replaySession, capacity)}
}

// Len reports how many sessions are tracked.
func (s *ReplayState) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Begin opens this turn's replay handle for a session and records how many
// messages the turn carried, so the NEXT turn knows where its frozen floor
// sits. Returns a handle that is safe for concurrent use.
func (s *ReplayState) Begin(sessionKey string, body []byte) *SessionReplay {
	count := int(gjson.GetBytes(body, "messages.#").Int())

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, seen := s.sessions[sessionKey]
	if !seen {
		if len(s.order) >= s.capacity {
			delete(s.sessions, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, sessionKey)
		sess = &replaySession{blocks: make(map[string]replayEntry)}
		s.sessions[sessionKey] = sess
	}
	sess.turn++
	sess.sweep()

	h := &SessionReplay{parent: s, sess: sess, floor: sess.sentCount, first: !seen, turn: sess.turn}
	sess.sentCount = count
	return h
}

// SessionReplay is one turn's view of one session's replay state.
type SessionReplay struct {
	parent *ReplayState
	sess   *replaySession
	floor  int
	first  bool
	turn   int
}

// FirstTurn reports whether this session had no state before this turn, which
// is also the case after an eviction or a proxy restart. Diagnostic only: it
// does not change the floor.
func (h *SessionReplay) FirstTurn() bool { return h.first }

// Floor returns the number of messages this session's previous turn carried,
// and 0 when there was no previous turn.
//
// A floor of 0 on a first turn is safe rather than reckless. The fresh
// compression pass only ever touches the LATEST user message, which is the one
// the client just appended, so the provider has never seen it whatever the
// floor says. The floor's real job starts on turn two: it stops the fresh pass
// re-reaching a message that WAS already sent — the case where a client ends
// its request with a prefilled assistant turn, leaving the latest user message
// an old one.
func (h *SessionReplay) Floor() int { return h.floor }

// Lookup returns the compressed text this session previously sent in place of
// the block whose original hashes to originalHash.
//
// A hit marks the entry as seen this turn, which is what keeps it out of the
// staleness sweep: the client is still sending that block.
func (h *SessionReplay) Lookup(originalHash string) (string, bool) {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	e, ok := h.sess.blocks[originalHash]
	if !ok {
		return "", false
	}
	e.seen = h.turn
	h.sess.blocks[originalHash] = e
	return e.text, true
}

// Record remembers that this session sent compressed in place of the block
// whose original hashes to originalHash, so every later turn reproduces it.
func (h *SessionReplay) Record(originalHash, compressed string) {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	h.sess.blocks[originalHash] = replayEntry{text: compressed, seen: h.turn}
}

// Forget drops a block's entry, so the next turn compresses it fresh instead
// of replaying text whose CCR original the store can no longer resolve.
func (h *SessionReplay) Forget(originalHash string) {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	delete(h.sess.blocks, originalHash)
}

// EntryCount reports how many blocks this session is holding, for tests and
// for the memory measurement.
func (h *SessionReplay) EntryCount() int {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	return len(h.sess.blocks)
}
