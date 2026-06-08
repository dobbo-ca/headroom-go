package backends

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func TestSQLitePutGet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	s, err := newSQLite(p, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.Put("a", "alpha")
	if v, ok := s.Get("a"); !ok || v != "alpha" {
		t.Fatalf("Get(a) = %q,%v", v, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("Get(missing) should be false")
	}
}

func TestSQLiteTTLExpiry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	s, err := newSQLite(p, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	s.Put("a", "1")
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected entry expired by TTL")
	}
}

func TestSQLiteRegisteredFactory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ccr.db")
	st, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.SQLite, Path: p})
	if err != nil {
		t.Fatal(err)
	}
	st.Put("k", "v")
	if v, ok := st.Get("k"); !ok || v != "v" {
		t.Fatalf("factory store Get = %q,%v", v, ok)
	}
}
