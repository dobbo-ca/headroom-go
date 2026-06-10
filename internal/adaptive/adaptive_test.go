package adaptive

import "testing"

func mk(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('a'+i%26)) + "-line"
	}
	return out
}

func TestFastPathKeepsAll(t *testing.T) {
	if got := ComputeOptimalK(mk(8), 0, 5, 30); got != 8 {
		t.Fatalf("n<=8 must return n unclamped, got %d", got)
	}
}

func TestNearTotalRedundancy(t *testing.T) {
	items := []string{"a", "a", "a", "a", "a", "a", "a", "a", "a", "a"} // unique=1
	if got := ComputeOptimalK(items, 0, 5, 30); got != 5 {              // max(minK,1)=5, min(5,30)=5
		t.Fatalf("redundant -> minK, got %d", got)
	}
}

func TestClampToMax(t *testing.T) {
	got := ComputeOptimalK(mk(100), 0, 5, 30)
	if got < 5 || got > 30 {
		t.Fatalf("k out of [5,30]: %d", got)
	}
}
