     1	package ccr
     2	
     3	import (
     4		"fmt"
     5		"time"
     6	)
     7	
     8	// Store holds CCR originals keyed by ComputeKey output.
     9	type Store interface {
    10		Put(hash, payload string)
    11		Get(hash string) (string, bool)
    12		// Len reports the number of stored entries. Expiry is lazy (enforced on
    13		// Get), so Len may include expired-but-not-yet-evicted entries.
    14		Len() int
    15	}
    16	
    17	const (
    18		DefaultCapacity = 1000
    19		DefaultTTL      = 5 * time.Minute
    20	)
    21	
    22	// BackendKind selects a Store implementation.
    23	type BackendKind int
    24	
    25	const (
    26		InMemory BackendKind = iota
    27		SQLite
    28	)
    29	
    30	// BackendConfig configures a Store. Capacity applies to InMemory (FIFO cap);
    31	// SQLite is TTL-only (no capacity cap) — preserve this asymmetry.
    32	type BackendConfig struct {
    33		Kind       BackendKind
    34		Capacity   int
    35		TTLSeconds uint64
    36		Path       string // SQLite file path
    37	}
    38	
    39	// newInMemory and newSQLite are wired by the backends package via Register to
    40	// avoid an import cycle (ccr/backends imports ccr, not vice-versa).
    41	var (
    42		newInMemory func(capacity int, ttl time.Duration) Store
    43		newSQLite   func(path string, ttl time.Duration) (Store, error)
    44	)
    45	
    46	// RegisterInMemory and RegisterSQLite are called from backends' init().
    47	func RegisterInMemory(f func(capacity int, ttl time.Duration) Store)       { newInMemory = f }
    48	func RegisterSQLite(f func(path string, ttl time.Duration) (Store, error)) { newSQLite = f }
    49	
    50	// FromConfig builds a Store. Import the backends package (blank import) before
    51	// calling so the constructors are registered.
    52	func FromConfig(cfg BackendConfig) (Store, error) {
    53		ttl := DefaultTTL
    54		if cfg.TTLSeconds > 0 {
    55			ttl = time.Duration(cfg.TTLSeconds) * time.Second
    56		}
    57		switch cfg.Kind {
    58		case InMemory:
    59			if newInMemory == nil {
    60				return nil, fmt.Errorf("ccr: in-memory backend not registered (blank-import internal/ccr/backends)")
    61			}
    62			cap := cfg.Capacity
    63			if cap <= 0 {
    64				cap = DefaultCapacity
    65			}
    66			return newInMemory(cap, ttl), nil
    67		case SQLite:
    68			if newSQLite == nil {
    69				return nil, fmt.Errorf("ccr: sqlite backend not registered (blank-import internal/ccr/backends)")
    70			}
    71			return newSQLite(cfg.Path, ttl)
    72		default:
    73			return nil, fmt.Errorf("ccr: unknown backend kind %d", cfg.Kind)
    74		}
    75	}
