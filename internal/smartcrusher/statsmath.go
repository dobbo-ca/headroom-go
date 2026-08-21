package smartcrusher

import (
	"math"
	"sort"
)

// Leaf numeric statistics mirroring Python's `statistics` module. No deps, no
// markers, no string math, no seam. Each helper returns (value, ok); ok=false
// signals "no meaningful result" (empty/too-few/non-finite), mapping the
// upstream Option<f64> [ref: stats_math.rs].
//
// DEFERRED (Plan 4+): formatG / normalizeScientificExp — Python-{:.4g}-style
// debug strings, not on the byte-saving path. Decision B rejects CPython %g
// parity, so they land with their only consumer (crush_number_array) later.

// isFinite reports whether x is neither infinite nor NaN.
func isFinite(x float64) bool {
	return !math.IsInf(x, 0) && !math.IsNaN(x)
}

// mean returns the arithmetic mean via a sequential left-to-right sum. Empty
// input or a non-finite result (overflow to Inf, or NaN) yields ok=false.
func mean(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	m := sum / float64(len(values))
	if !isFinite(m) {
		return 0, false
	}
	return m, true
}

// sampleVariance returns the Bessel-corrected (n-1) sample variance. Fewer than
// two values, or a non-finite mean/result, yields ok=false. Squaring uses
// (v-m)*(v-m), matching Rust's .powi(2) repeated multiply (NOT math.Pow).
func sampleVariance(values []float64) (float64, bool) {
	if len(values) < 2 {
		return 0, false
	}
	m, ok := mean(values)
	if !ok {
		return 0, false
	}
	sumSqDiff := 0.0
	for _, v := range values {
		d := v - m
		sumSqDiff += d * d
	}
	variance := sumSqDiff / float64(len(values)-1)
	if !isFinite(variance) {
		return 0, false
	}
	return variance, true
}

// sampleStdev returns the square root of the sample variance, propagating
// ok=false. sqrt of a finite non-negative variance is finite, so no re-check.
func sampleStdev(values []float64) (float64, bool) {
	variance, ok := sampleVariance(values)
	if !ok {
		return 0, false
	}
	return math.Sqrt(variance), true
}

// median sorts a COPY (caller slice untouched) and returns the middle value for
// odd n, or the mean of the two middles for even n. Empty input yields ok=false.
func median(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 0 {
		return (cp[n/2-1] + cp[n/2]) / 2.0, true
	}
	return cp[n/2], true
}
