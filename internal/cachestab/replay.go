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
// ponytail: entries are never removed within a session, so a block that
// scrolls out of the conversation stays in the map until the session is
// evicted. Bound it per session only if a measurement shows it matters.
type ReplayState struct {
	mu       sync.Mutex
	capacity int
	sessions map[string]*replaySession
	order    []string
}

// replaySession is one conversation's replay state.
type replaySession struct {
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
	blocks map[string]string
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
		sess = &replaySession{blocks: make(map[string]string)}
		s.sessions[sessionKey] = sess
	}

	h := &SessionReplay{parent: s, sess: sess, floor: sess.sentCount, first: !seen}
	sess.sentCount = count
	return h
}

// SessionReplay is one turn's view of one session's replay state.
type SessionReplay struct {
	parent *ReplayState
	sess   *replaySession
	floor  int
	first  bool
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
func (h *SessionReplay) Lookup(originalHash string) (string, bool) {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	v, ok := h.sess.blocks[originalHash]
	return v, ok
}

// Record remembers that this session sent compressed in place of the block
// whose original hashes to originalHash, so every later turn reproduces it.
func (h *SessionReplay) Record(originalHash, compressed string) {
	h.parent.mu.Lock()
	defer h.parent.mu.Unlock()
	h.sess.blocks[originalHash] = compressed
}
