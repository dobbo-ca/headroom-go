package ccr

import "testing"

func TestComputeKeyMD5(t *testing.T) {
	// Verified upstream vectors (hashlib.md5(s).hexdigest()[:24]):
	cases := map[string]string{
		"hello": "5d41402abc4b2a76b9719d91",
		"":      "d41d8cd98f00b204e9800998",
	}
	for in, want := range cases {
		if got := ComputeKeyMD5([]byte(in)); got != want {
			t.Errorf("ComputeKeyMD5(%q) = %q, want %q", in, got, want)
		}
	}
	if len(ComputeKeyMD5([]byte("anything"))) != 24 {
		t.Fatal("key must be 24 hex chars")
	}
}
