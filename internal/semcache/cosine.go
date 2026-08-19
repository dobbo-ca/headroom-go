package semcache

import "math"

// Cosine returns the cosine similarity of a and b, in [-1, 1].
//
// It returns 0 for mismatched lengths, empty input, or a zero-magnitude
// vector. Those are all "no useful comparison", and 0 is far below any usable
// threshold, so a caller that ignores the distinction still behaves correctly.
//
// Accumulate in float64: a float32 sum over a 768-dimension vector loses
// enough precision to move a score across a 0.97 threshold.
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// float32bits is math.Float32bits, named locally so cache.go does not import
// math just for one conversion.
func float32bits(f float32) uint32 { return math.Float32bits(f) }
