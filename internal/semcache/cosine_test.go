package semcache

import "testing"

func nearly(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

func TestCosineIdenticalVectorsIsOne(t *testing.T) {
	v := []float32{1, 2, 3}
	if got := Cosine(v, v); !nearly(got, 1) {
		t.Errorf("Cosine = %v, want 1", got)
	}
}

func TestCosineOrthogonalIsZero(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); !nearly(got, 0) {
		t.Errorf("Cosine = %v, want 0", got)
	}
}

func TestCosineOppositeIsMinusOne(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{-1, 0}); !nearly(got, -1) {
		t.Errorf("Cosine = %v, want -1", got)
	}
}

func TestCosineIgnoresMagnitude(t *testing.T) {
	if got := Cosine([]float32{1, 2}, []float32{10, 20}); !nearly(got, 1) {
		t.Errorf("Cosine = %v, want 1", got)
	}
}

func TestCosineLengthMismatchIsZero(t *testing.T) {
	if got := Cosine([]float32{1, 2, 3}, []float32{1, 2}); got != 0 {
		t.Errorf("Cosine = %v, want 0 for mismatched lengths", got)
	}
}

func TestCosineZeroVectorIsZero(t *testing.T) {
	if got := Cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("Cosine = %v, want 0 for a zero vector", got)
	}
}

func TestCosineEmptyIsZero(t *testing.T) {
	if got := Cosine(nil, nil); got != 0 {
		t.Errorf("Cosine = %v, want 0 for empty vectors", got)
	}
}
