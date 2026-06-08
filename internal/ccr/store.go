package ccr

import (
	"fmt"
	"time"
)

// Store holds CCR originals keyed by ComputeKey output.
type Store interface {
	Put(hash, payload string)
	Get(hash string) (string, bool)
	Len() int
}

const (
	DefaultCapacity = 1000
	DefaultTTL      = 5 * time.Minute
)

// BackendKind selects a Store implementation.
type BackendKind int

const (
	InMemory BackendKind = iota
	SQLite
)

// BackendConfig configures a Store. Capacity applies to InMemory (FIFO cap);
// SQLite is TTL-only (no capacity cap) — preserve this asymmetry.
type BackendConfig struct {
	Kind       BackendKind
	Capacity   int
	TTLSeconds uint64
	Path       string // SQLite file path
}

// newInMemory and newSQLite are wired by the backends package via Register to
// avoid an import cycle (ccr/backends imports ccr, not vice-versa).
var (
	newInMemory func(capacity int, ttl time.Duration) Store
	newSQLite   func(path string, ttl time.Duration) (Store, error)
)

// RegisterInMemory and RegisterSQLite are called from backends' init().
func RegisterInMemory(f func(capacity int, ttl time.Duration) Store)       { newInMemory = f }
func RegisterSQLite(f func(path string, ttl time.Duration) (Store, error)) { newSQLite = f }

// FromConfig builds a Store. Import the backends package (blank import) before
// calling so the constructors are registered.
func FromConfig(cfg BackendConfig) (Store, error) {
	ttl := DefaultTTL
	if cfg.TTLSeconds > 0 {
		ttl = time.Duration(cfg.TTLSeconds) * time.Second
	}
	switch cfg.Kind {
	case InMemory:
		if newInMemory == nil {
			return nil, fmt.Errorf("ccr: in-memory backend not registered (blank-import internal/ccr/backends)")
		}
		cap := cfg.Capacity
		if cap <= 0 {
			cap = DefaultCapacity
		}
		return newInMemory(cap, ttl), nil
	case SQLite:
		if newSQLite == nil {
			return nil, fmt.Errorf("ccr: sqlite backend not registered (blank-import internal/ccr/backends)")
		}
		return newSQLite(cfg.Path, ttl)
	default:
		return nil, fmt.Errorf("ccr: unknown backend kind %d", cfg.Kind)
	}
}
