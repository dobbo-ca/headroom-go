package smartcrusher

import (
	"strings"
	"testing"
)

// TestHashFieldName pins the verified SHA-256[:8] reference vectors. Truncation
// MUST stay at 8 hex chars: a prior [:16] silently broke all preserve-field
// lookups [ref: hashing.rs edgeCases]. Unicode hashes over UTF-8 bytes, not runes.
func TestHashFieldName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"customer_id", "1e38d67d"},
		{"", "e3b0c442"},
		{"café", "850f7dc4"},
		{"test", "9f86d081"},
	}
	for _, c := range cases {
		if got := HashFieldName(c.name); got != c.want {
			t.Errorf("HashFieldName(%q): want %q, got %q", c.name, c.want, got)
		}
	}
}

// TestHashFieldNameLength guards truncation drift: any input, however long,
// yields exactly 8 hex chars.
func TestHashFieldNameLength(t *testing.T) {
	if got := len(HashFieldName(strings.Repeat("x", 1000))); got != 8 {
		t.Errorf("len(HashFieldName(1000x)): want 8, got %d", got)
	}
}
