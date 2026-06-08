package backends

import (
	"testing"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

func TestInMemoryPutGet(t *testing.T) {
	s := newInMemory(2, time.Minute)
	s.Put("a", "alpha")
	if v, ok := s.Get("a"); !ok || v != "alpha" {
		t.Fatalf("Get(a) = %q,%v", v, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("Get(missing) should be false")
	}
}

func TestInMemoryFIFOEviction(t *testing.T) {
	s := newInMemory(2, time.Minute)
	s.Put("a", "1")
	s.Put("b", "2")
	s.Put("c", "3") // evicts "a"
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected oldest entry evicted")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("newest entry should be present")
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
}

func TestInMemoryTTLExpiry(t *testing.T) {
	s := newInMemory(10, 10*time.Millisecond)
	t.Cleanup(func() { timeNow = time.Now })
	timeNow = func() time.Time { return time.Unix(0, 0) }
	s.Put("a", "1")
	timeNow = func() time.Time { return time.Unix(100, 0) } // past the TTL
	if _, ok := s.Get("a"); ok {
		t.Fatal("expected entry expired by TTL")
	}
}

func TestInMemoryRegisteredFactory(t *testing.T) {
	st, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 4})
	if err != nil {
		t.Fatal(err)
	}
	st.Put("k", "v")
	if v, ok := st.Get("k"); !ok || v != "v" {
		t.Fatalf("factory store Get = %q,%v", v, ok)
	}
}
