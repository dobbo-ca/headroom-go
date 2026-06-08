// Package backends provides CCR Store implementations. Blank-import this package
// to register them with ccr.FromConfig.
package backends

import (
	"container/list"
	"sync"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func init() { ccr.RegisterInMemory(func(c int, ttl time.Duration) ccr.Store { return newInMemory(c, ttl) }) }

type entry struct {
	hash    string
	payload string
	expires time.Time
}

type inMemory struct {
	mu    sync.Mutex
	cap   int
	ttl   time.Duration
	items map[string]*list.Element // hash -> element holding *entry
	order *list.List               // front = oldest
}

func newInMemory(capacity int, ttl time.Duration) *inMemory {
	return &inMemory{cap: capacity, ttl: ttl, items: make(map[string]*list.Element), order: list.New()}
}

// Put inserts or updates an entry. Re-Putting an existing key updates its
// payload and TTL in place and does NOT move it to the back of the FIFO order:
// eviction is by insertion order, not recency — this is intentional.
func (m *inMemory) Put(hash, payload string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[hash]; ok {
		el.Value.(*entry).payload = payload
		el.Value.(*entry).expires = m.deadline()
		return
	}
	el := m.order.PushBack(&entry{hash: hash, payload: payload, expires: m.deadline()})
	m.items[hash] = el
	for m.order.Len() > m.cap {
		oldest := m.order.Front()
		m.order.Remove(oldest)
		delete(m.items, oldest.Value.(*entry).hash)
	}
}

func (m *inMemory) Get(hash string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[hash]
	if !ok {
		return "", false
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && timeNow().After(e.expires) {
		m.order.Remove(el)
		delete(m.items, hash)
		return "", false
	}
	return e.payload, true
}

func (m *inMemory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order.Len()
}

func (m *inMemory) deadline() time.Time {
	if m.ttl <= 0 {
		return time.Time{}
	}
	return timeNow().Add(m.ttl)
}

// timeNow is a package var so tests stay deterministic if needed; it is NOT on
// the compression path (CCR storage is side-channel, not subject to I4).
var timeNow = time.Now
