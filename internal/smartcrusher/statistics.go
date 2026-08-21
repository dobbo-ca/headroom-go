package smartcrusher

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Pure-math field-characterization helpers (port of smart_crusher.py:378-481
// via smart_crusher/statistics.rs). Despite the module name these are NOT
// percentile / SmartSample statistics; there are no CCR markers, byte math, or
// orderedmap here. Key subtlety by design: IsUUIDFormat measures BYTE length
// while CalculateStringEntropy counts RUNES [ref: statistics.rs].
//
// DEFERRED (Plan 4+): percentileLinear / roundTiesEven / formatNumberRepr /
// finiteMin / finiteMax (the crushers.rs MVP-shared carve-out) have no MVP
// consumer — they feed only crush_number_array and compute_k_split, both
// deferred — and formatNumberRepr is a format-strategy-string helper Decision B
// forbids replicating. They land with their consumer in a later plan.

// UUID structural constants [ref: statistics.rs constants].
const (
	uuidTotalLen     = 36
	uuidSegmentCount = 5
	entropyMinLen    = 2
	seqMinValues     = 5
	seqDiffLo        = 0.5
	seqDiffHi        = 2.0
	seqMinPairwise   = 2
)

// uuidSegmentLens is the canonical 8-4-4-4-12 hyphen-delimited segment layout.
var uuidSegmentLens = [uuidSegmentCount]int{8, 4, 4, 4, 12}

// IsUUIDFormat reports whether value is a canonical 36-byte 8-4-4-4-12 hex UUID.
// Length is measured in BYTES (Rust .len()), hex is case-insensitive, and no
// version/variant bits are validated [ref: statistics.rs algorithm].
func IsUUIDFormat(value string) bool {
	if len(value) != uuidTotalLen {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) != uuidSegmentCount {
		return false
	}
	for i, part := range parts {
		if len(part) != uuidSegmentLens[i] {
			return false
		}
		for j := 0; j < len(part); j++ {
			if !isASCIIHexDigit(part[j]) {
				return false
			}
		}
	}
	return true
}

// isASCIIHexDigit reports whether b is 0-9, a-f, or A-F.
func isASCIIHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// CalculateStringEntropy returns the normalized Shannon entropy over RUNES (code
// points), in [0,1]. Strings shorter than two runes, or with a single distinct
// rune, return 0.0 (maxEntropy log2(1)=0 is guarded) [ref: statistics.rs
// algorithm]. Rune-level is correct here (a statistical feature, not byte math).
func CalculateStringEntropy(s string) float64 {
	runes := []rune(s)
	n := len(runes)
	if n < entropyMinLen {
		return 0.0
	}
	freq := make(map[rune]int, n)
	for _, r := range runes {
		freq[r]++
	}
	length := float64(n)
	entropy := 0.0
	for _, count := range freq {
		if count > 0 {
			p := float64(count) / length
			entropy -= p * math.Log2(p)
		}
	}
	distinct := len(freq)
	m := distinct
	if n < m {
		m = n
	}
	maxEntropy := math.Log2(float64(m))
	if maxEntropy > 0.0 {
		return entropy / maxEntropy
	}
	return 0.0
}

// DetectSequentialPattern reports whether values form a near-arithmetic sequence
// with a unit-ish step (avg diff in [0.5,2.0]). BUG#2 fixed: purely-string
// numerics are treated as categorical and rejected — at least one real JSON
// Number must be present [ref: statistics.rs algorithm, BUG#2]. When checkOrder
// is set, the ORIGINAL (unsorted) order must be mostly ascending too.
func DetectSequentialPattern(values []any, checkOrder bool) bool {
	if len(values) < seqMinValues {
		return false
	}

	nums := make([]float64, 0, len(values))
	hadNonStringNumeric := false
	for _, v := range values {
		switch t := v.(type) {
		case json.Number:
			if f, err := t.Float64(); err == nil {
				nums = append(nums, f)
				hadNonStringNumeric = true
			}
		case bool:
			// bools are never numeric (Python isinstance(bool) exclusion).
		case string:
			if n, ok := pythonIntParse(t); ok {
				nums = append(nums, float64(n))
				// intentionally do NOT set hadNonStringNumeric.
			}
		default:
			// null / array / object -> skip.
		}
	}

	if len(nums) < seqMinValues {
		return false
	}
	// BUG#2 gate: all-string numerics are categorical -> not sequential.
	if !hadNonStringNumeric {
		return false
	}
	if len(nums) < seqMinPairwise {
		return false
	}

	sorted := append([]float64(nil), nums...)
	sort.SliceStable(sorted, func(i, j int) bool {
		// partial_cmp with NaN treated as Equal (never reports Less).
		return sorted[i] < sorted[j]
	})

	diffs := make([]float64, 0, len(sorted)-1)
	for i := 1; i < len(sorted); i++ {
		diffs = append(diffs, sorted[i]-sorted[i-1])
	}
	if len(diffs) == 0 {
		return false
	}

	sum := 0.0
	for _, d := range diffs {
		sum += d
	}
	avgDiff := sum / float64(len(diffs))
	if !(seqDiffLo <= avgDiff && avgDiff <= seqDiffHi) {
		return false
	}

	consistentCount := 0
	for _, d := range diffs {
		if seqDiffLo <= d && d <= seqDiffHi {
			consistentCount++
		}
	}
	if !(float64(consistentCount)/float64(len(diffs)) > 0.8) {
		return false
	}

	if checkOrder {
		ascendingCount := 0
		for i := 1; i < len(nums); i++ {
			if nums[i-1] <= nums[i] {
				ascendingCount++
			}
		}
		nPairs := len(nums) - 1
		return float64(ascendingCount)/float64(nPairs) > 0.7
	}
	return true
}

// pythonIntParse mirrors CPython int(): trims surrounding whitespace, accepts a
// leading +/- sign and single underscores between digits, and rejects
// leading/trailing/double underscores, floats, hex, and scientific notation
// [ref: statistics.rs algorithm].
func pythonIntParse(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, "_") {
		if s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
			return 0, false
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
