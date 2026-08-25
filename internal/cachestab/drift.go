package cachestab

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// EarlyMessagesWindow is how many leading messages count as the settled
// prefix. Anything past it is the live zone, where mutation is expected and
// benign, so hashing it would report drift on every single turn.
const EarlyMessagesWindow = 3

// DefaultDriftCapacity bounds the session map. A proxy runs for days; an
// unbounded map is a leak.
const DefaultDriftCapacity = 1024

// opaquePayloadKeys hold user data rather than protocol structure. A
// cache_control key inside one of these is the customer's own field, not a
// cache breakpoint, so stripping it would make two genuinely different
// payloads hash alike and mask real drift.
var opaquePayloadKeys = map[string]bool{
	"input": true, "arguments": true, "json": true, "input_schema": true,
}

// StructuralHash fingerprints the cache hot zone on three axes. Equality is
// stricter than drift: a conversation growing into an empty early slot
// compares unequal but is not drift. Use Drifted for the decision.
type StructuralHash struct {
	System        [32]byte
	Tools         [32]byte
	EarlyMessages [EarlyMessagesWindow]*[32]byte
}

// ComputeStructuralHash fingerprints an Anthropic /v1/messages body. It takes
// bytes and returns a hash: there is no path by which it can alter a request.
func ComputeStructuralHash(body []byte) StructuralHash {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Malformed JSON still gets a stable fingerprint, so a first
		// request is well defined and a later valid body reads as drift.
		return StructuralHash{System: sha256.Sum256(nil), Tools: sha256.Sum256(nil)}
	}

	h := StructuralHash{
		System: hashCanonical(parsed["system"]),
		Tools:  hashCanonical(parsed["tools"]),
	}
	for i, msg := range conversationMessages(parsed["messages"]) {
		if i >= EarlyMessagesWindow {
			break
		}
		sum := hashCanonical(msg)
		h.EarlyMessages[i] = &sum
	}
	return h
}

// conversationMessages returns the raw message elements, or nil.
func conversationMessages(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	return msgs
}

// hashCanonical canonicalises then SHA-256s a subtree. Absent fields hash as
// JSON null rather than being skipped, so "system removed" is distinguishable
// from "system unchanged".
func hashCanonical(raw json.RawMessage) [32]byte {
	if len(raw) == 0 {
		return sha256.Sum256([]byte("null"))
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return sha256.Sum256(raw)
	}
	// encoding/json marshals map keys in sorted order, so re-marshalling is
	// already whitespace- and key-order-neutral. Only the cache_control
	// strip has to be done by hand.
	canonical, err := json.Marshal(stripCacheControl(v, false))
	if err != nil {
		return sha256.Sum256(raw)
	}
	return sha256.Sum256(canonical)
}

// stripCacheControl removes cache_control members outside opaque payloads.
//
// A cache breakpoint is placement metadata, not structure. Clients relocate it
// to the newest block every turn — Claude Code does exactly this — and moving
// a breakpoint never invalidates an already-cached prefix. Hashing it would
// report drift on every single turn and make the detector useless.
func stripCacheControl(v any, inOpaque bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			if !inOpaque && k == "cache_control" {
				continue
			}
			out[k] = stripCacheControl(sub, inOpaque || opaquePayloadKeys[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = stripCacheControl(sub, inOpaque)
		}
		return out
	default:
		return v
	}
}

// Drifted reports which axes were rewritten between two turns of one session,
// in a fixed order so a log query can match deterministically. An empty slice
// means the prefix held.
func (prev StructuralHash) Drifted(curr StructuralHash) []string {
	var dims []string
	if prev.System != curr.System {
		dims = append(dims, "system")
	}
	if prev.Tools != curr.Tools {
		dims = append(dims, "tools")
	}
	if earlyWindowDrifted(prev.EarlyMessages, curr.EarlyMessages) {
		dims = append(dims, "early_messages")
	}
	return dims
}

// earlyWindowDrifted is prefix-aware. A settled slot that changes or
// disappears means the prefix the provider cached was rewritten: real drift,
// real money. A conversation growing into a slot that was empty is
// append-only and benign.
func earlyWindowDrifted(prev, curr [EarlyMessagesWindow]*[32]byte) bool {
	for i := range prev {
		switch {
		case prev[i] == nil:
			continue
		case curr[i] == nil:
			return true
		case *prev[i] != *curr[i]:
			return true
		}
	}
	return false
}

// Observation is what one Observe call concluded.
type Observation struct {
	// FirstRequest is true when this session had no baseline yet.
	FirstRequest bool
	// Dims lists the drifted axes. Empty means the prefix held.
	Dims []string
	// SessionDigest is a short hash of the session key, safe to log. The
	// key itself never appears in a log line.
	SessionDigest string
}

// Drifted reports whether this observation found a busted cache prefix.
func (o Observation) Drifted() bool { return !o.FirstRequest && len(o.Dims) > 0 }

// DriftState tracks the last fingerprint per session. Safe for concurrent use.
//
// Eviction is FIFO rather than LRU: a proxy sees sessions arrive and go quiet,
// so insertion order approximates recency closely enough, and it avoids a
// dependency for a bookkeeping detail.
//
// ponytail: FIFO eviction. Swap for true LRU only if a measurement shows
// long-lived sessions being evicted under short-lived churn.
type DriftState struct {
	mu       sync.Mutex
	capacity int
	seen     map[string]StructuralHash
	order    []string
}

// NewDriftState builds a DriftState. A capacity of zero or less selects
// DefaultDriftCapacity.
func NewDriftState(capacity int) *DriftState {
	if capacity <= 0 {
		capacity = DefaultDriftCapacity
	}
	return &DriftState{capacity: capacity, seen: make(map[string]StructuralHash, capacity)}
}

// Observe records this turn's fingerprint for a session and reports what
// changed since the previous turn.
func (s *DriftState) Observe(sessionKey string, curr StructuralHash) Observation {
	obs := Observation{SessionDigest: shortDigest(sessionKey)}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, ok := s.seen[sessionKey]
	if !ok {
		obs.FirstRequest = true
		if len(s.order) >= s.capacity {
			delete(s.seen, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, sessionKey)
	} else {
		obs.Dims = prev.Drifted(curr)
	}
	s.seen[sessionKey] = curr
	return obs
}

// Len reports how many sessions are tracked.
func (s *DriftState) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// shortDigest hashes a value to 16 hex characters. Used for anything derived
// from a credential, so the raw value cannot reach a log line.
func shortDigest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:16]
}
