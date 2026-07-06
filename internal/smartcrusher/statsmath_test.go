package smartcrusher

import (
	"math"
	"testing"
)

// TestMean covers the empty/single/overflow edge cases. Overflow to +Inf is
// non-finite -> ok=false; NaN is rejected too [ref: stats_math.rs edgeCases].
func TestMean(t *testing.T) {
	if _, ok := mean(nil); ok {
		t.Errorf("mean([]): want ok=false")
	}
	if m, ok := mean([]float64{5}); !ok || m != 5 {
		t.Errorf("mean([5]): want (5,true), got (%v,%v)", m, ok)
	}
	if _, ok := mean([]float64{1e308, 1e308}); ok {
		t.Errorf("mean([1e308,1e308]): want ok=false (Inf overflow)")
	}
	if _, ok := mean([]float64{math.NaN()}); ok {
		t.Errorf("mean([NaN]): want ok=false")
	}
}

// TestSampleVariance uses Bessel (n-1). len<2 -> false; all-identical -> 0.0;
// overflow -> false [ref: stats_math.rs edgeCases].
func TestSampleVariance(t *testing.T) {
	if _, ok := sampleVariance(nil); ok {
		t.Errorf("sampleVariance([]): want ok=false")
	}
	if _, ok := sampleVariance([]float64{5}); ok {
		t.Errorf("sampleVariance([5]): want ok=false")
	}
	if v, ok := sampleVariance([]float64{3, 3, 3}); !ok || v != 0.0 {
		t.Errorf("sampleVariance([3,3,3]): want (0.0,true), got (%v,%v)", v, ok)
	}
	if _, ok := sampleVariance([]float64{1e200, -1e200}); ok {
		t.Errorf("sampleVariance([1e200,-1e200]): want ok=false (Inf)")
	}
}

// TestSampleStdev propagates false from variance [ref: stats_math.rs edgeCases].
func TestSampleStdev(t *testing.T) {
	if _, ok := sampleStdev([]float64{5}); ok {
		t.Errorf("sampleStdev([5]): want ok=false")
	}
	if _, ok := sampleStdev([]float64{1e200, -1e200}); ok {
		t.Errorf("sampleStdev([1e200,-1e200]): want ok=false")
	}
	if s, ok := sampleStdev([]float64{2, 4, 4, 4, 5, 5, 7, 9}); !ok {
		t.Errorf("sampleStdev valid: want ok=true, got ok=false")
	} else if math.Abs(s-2.138089935299395) > 1e-9 {
		t.Errorf("sampleStdev: want ~2.13809, got %v", s)
	}
}

// TestMedian: odd -> middle, even -> mean of two middles, single -> value;
// the input slice must not be mutated (sorts a copy) [ref: stats_math.rs].
func TestMedian(t *testing.T) {
	if _, ok := median(nil); ok {
		t.Errorf("median([]): want ok=false")
	}
	if m, ok := median([]float64{3, 1, 2}); !ok || m != 2 {
		t.Errorf("median([3,1,2]): want (2,true), got (%v,%v)", m, ok)
	}
	if m, ok := median([]float64{4, 1, 2, 3}); !ok || m != 2.5 {
		t.Errorf("median([4,1,2,3]): want (2.5,true), got (%v,%v)", m, ok)
	}
	if m, ok := median([]float64{42}); !ok || m != 42 {
		t.Errorf("median([42]): want (42,true), got (%v,%v)", m, ok)
	}

	in := []float64{3, 1, 2}
	_, _ = median(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Errorf("median mutated caller slice: got %v", in)
	}
}
