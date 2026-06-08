package ccr

import "testing"

func TestComputeKeyDeterministicAnd24Hex(t *testing.T) {
	a := ComputeKey([]byte("hello world"))
	b := ComputeKey([]byte("hello world"))
	if a != b {
		t.Fatalf("ComputeKey not deterministic: %q != %q", a, b)
	}
	if len(a) != 24 {
		t.Fatalf("key length = %d, want 24 hex chars", len(a))
	}
	for _, r := range a {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("key has non-lowercase-hex rune %q in %q", r, a)
		}
	}
	if ComputeKey([]byte("different")) == a {
		t.Fatal("distinct inputs produced the same key")
	}
}
