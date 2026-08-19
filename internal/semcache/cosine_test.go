package semcache

import (
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"magnitude ignored", []float32{1, 2}, []float32{10, 20}, 1},
		// The last three must not compare: 0 is far below any usable threshold.
		{"length mismatch", []float32{1, 2, 3}, []float32{1, 2}, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
		{"empty", nil, nil, 0},
	} {
		if got := Cosine(tc.a, tc.b); math.Abs(float64(got-tc.want)) > 1e-6 {
			t.Errorf("%s: Cosine = %v, want %v", tc.name, got, tc.want)
		}
	}
}
