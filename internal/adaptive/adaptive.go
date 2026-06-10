// Package adaptive provides the simplified adaptive sizer used by the log and
// search compressors to pick how many leading items/rows/lines to keep.
//
// This is the SIMPLIFIED sizer (spec §6). It faithfully ports the two cheap
// branches of upstream chopratejas/headroom compute_optimal_k:
//   - the n<=8 fast path (return raw n, NOT clamped), and
//   - the None-knee branch: keepFraction = 0.3 + 0.7*diversity, then
//     clamp(int(n*keepFraction), minK, effectiveMax).
//
// Dropped vs upstream (tracked as follow-ups, full compute_optimal_k):
//   - SimHash/MD5 fingerprinting + greedy Hamming clustering (simhash,
//     hamming_distance, count_unique_simhash);
//   - the bigram-coverage curve (compute_unique_bigram_curve);
//   - Kneedle knee detection (find_knee);
//   - zlib compression-ratio validation/boost (validate_with_zlib,
//     zlib_compressed_len).
//
// Diversity is approximated by exact-string uniqueness (a set of distinct
// items) instead of SimHash clusters; this changes the unique_count signal
// versus upstream but preserves the same clamp shape.
package adaptive

// ComputeOptimalK returns how many leading items to keep.
//
// bias is treated as a keep-multiplier where bias <= 0 means neutral (keep the
// diversity-scaled fraction). This is a documented divergence from upstream,
// which multiplies the knee by bias unconditionally; callers pass bias=0.0 by
// default, which here keeps the diversity-scaled fraction rather than
// collapsing to minK.
func ComputeOptimalK(items []string, bias float64, minK int, maxK int) int {
	n := len(items)

	effMax := maxK
	if effMax <= 0 {
		effMax = n
	}

	// Tier 1 fast path: keep everything, no clamp (faithful: returns raw n).
	if n <= 8 {
		return n
	}

	// Diversity via exact-string uniqueness (replaces SimHash clustering).
	seen := make(map[string]struct{}, n)
	for _, it := range items {
		seen[it] = struct{}{}
	}
	unique := len(seen)
	div := float64(unique) / float64(n)

	// Near-total redundancy short-circuit.
	if unique <= 3 {
		k := max(minK, unique)
		return min(k, effMax)
	}

	// None-knee branch (simplified — no Kneedle): diversity-scaled keep fraction.
	keepFraction := 0.3 + 0.7*div
	knee := max(minK, int(float64(n)*keepFraction))

	// Bias multiplier: bias <= 0 is neutral.
	k := knee
	if bias > 0 {
		k = max(minK, int(float64(knee)*bias))
	}

	// Final clamp: down to effMax first, then up to minK (minK wins if larger).
	return max(minK, min(k, effMax))
}
