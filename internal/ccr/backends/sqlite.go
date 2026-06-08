package backends

import (
	"database/sql"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "modernc.org/sqlite"
)

func init() {
	ccr.RegisterSQLite(func(path string, ttl time.Duration) (ccr.Store, error) { return newSQLite(path, ttl) })
}

type sqliteStore struct {
	db  *sql.DB
	ttl time.Duration
}

func newSQLite(path string, ttl time.Duration) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ccr (
		hash TEXT PRIMARY KEY,
		payload TEXT NOT NULL,
		expires_unix_ns INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db, ttl: ttl}, nil
}

func (s *sqliteStore) Put(hash, payload string) {
	var exp int64
	if s.ttl > 0 {
		exp = timeNow().Add(s.ttl).UnixNano()
	}
	_, _ = s.db.Exec(
		`INSERT INTO ccr(hash,payload,expires_unix_ns) VALUES(?,?,?)
		 ON CONFLICT(hash) DO UPDATE SET payload=excluded.payload, expires_unix_ns=excluded.expires_unix_ns`,
		hash, payload, exp,
	)
}

func (s *sqliteStore) Get(hash string) (string, bool) {
	var payload string
	var exp int64
	err := s.db.QueryRow(`SELECT payload, expires_unix_ns FROM ccr WHERE hash=?`, hash).Scan(&payload, &exp)
	if err != nil {
		return "", false
	}
	if exp != 0 && timeNow().UnixNano() > exp {
		_, _ = s.db.Exec(`DELETE FROM ccr WHERE hash=?`, hash)
		return "", false
	}
	return payload, true
}

func (s *sqliteStore) Len() int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ccr`).Scan(&n); err != nil {
		return 0
	}
	return n
}
