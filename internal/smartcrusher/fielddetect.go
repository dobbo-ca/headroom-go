package smartcrusher

import (
	"encoding/json"
	"math"

	"github.com/iancoleman/orderedmap"
)

// Two pure boolean+confidence detectors that label a column as ID-like or
// score-like [ref: field_detect.rs]. No CCR markers, no byte math: the
// confidences are fixed literal sums (Decision B — no float-format quirks).
// Number coercion follows Rust as_f64 semantics (JSON numbers yes, strings and
// bools NO); the finite guard rejects NaN/Inf.

// Field-detection thresholds [ref: (d) FIELD_DETECT].
const (
	idUniqueRatioGate     = 0.9  // strict < -> not an ID at all.
	idUUIDSampleSize      = 20   // take(20) THEN filter strings.
	idUUIDFraction        = 0.8  // uuidCount/len > this -> conf 0.95.
	idEntropyThreshold    = 0.7  // avgEntropy > this (with uniq>0.95) -> conf 0.8.
	idEntropyUniqueRatio  = 0.95 // strict > for entropy/sequential/range branches.
	idCatchAllUniqueRatio = 0.98 // strict > -> conf 0.7.

	scoreSampleSeqSize   = 50   // sequential-rejection sample.
	scoreDescendingMin   = 5    // descending bonus requires >=5 values.
	scoreDescendingFrac  = 0.7  // descendingCount/numPairs > this -> +0.3.
	scoreFloatSampleSize = 20   // float-fraction sample.
	scoreFloatFraction   = 0.3  // floatCount > len*this -> +0.1.
	scoreDecisionThresh  = 0.4  // isScore = confidence >= this.
	scoreConfidenceCap   = 0.95 // final confidence clamp.
)

// DetectIDFieldStatistically reports whether the field is an identifier column
// and, if so, a confidence in [0,0.95]. The UniqueRatio<0.9 hard gate short-
// circuits first; then string (UUID / high-entropy) and numeric (sequential /
// range) branches, then a >0.98-uniqueness catch-all [ref: field_detect.rs
// detect_id]. All uniqueness comparisons are strict.
func DetectIDFieldStatistically(stats FieldStats, values []any) (bool, float64) {
	// STEP1 hard gate.
	if stats.UniqueRatio < idUniqueRatioGate {
		return false, 0.0
	}

	// STEP2 string branch: sample take(20) THEN filter-is-string (a non-string
	// in the first 20 consumes a slot, so the string sample can be <20).
	if stats.FieldType == "string" {
		limit := idUUIDSampleSize
		if len(values) < limit {
			limit = len(values)
		}
		strSample := make([]string, 0, limit)
		for i := 0; i < limit; i++ {
			if s, ok := values[i].(string); ok {
				strSample = append(strSample, s)
			}
		}
		if len(strSample) > 0 {
			uuidCount := 0
			entropySum := 0.0
			for _, s := range strSample {
				if IsUUIDFormat(s) {
					uuidCount++
				}
				entropySum += CalculateStringEntropy(s)
			}
			if float64(uuidCount)/float64(len(strSample)) > idUUIDFraction {
				return true, 0.95
			}
			avgEntropy := entropySum / float64(len(strSample))
			if avgEntropy > idEntropyThreshold && stats.UniqueRatio > idEntropyUniqueRatio {
				return true, 0.8
			}
		}
	}

	// STEP3 numeric branch. The sequential helper receives the FULL unfiltered
	// values (it filters internally).
	if stats.FieldType == "numeric" {
		if DetectSequentialPattern(values, true) && stats.UniqueRatio > idEntropyUniqueRatio {
			return true, 0.9
		}
		if stats.MinVal != nil && stats.MaxVal != nil {
			valueRange := *stats.MaxVal - *stats.MinVal
			if valueRange > 0.0 && stats.UniqueRatio > idEntropyUniqueRatio {
				return true, 0.85
			}
		}
	}

	// STEP4 catch-all.
	if stats.UniqueRatio > idCatchAllUniqueRatio {
		return true, 0.7
	}

	// STEP5.
	return false, 0.0
}

// DetectScoreFieldStatistically reports whether the field is a score/relevance
// column and, if so, a confidence in [0,0.95]. It requires a numeric field with
// both min and max set, accumulates range/descending/float bonuses, and rejects
// sequential columns [ref: field_detect.rs detect_score]. The [-1,1] range check
// is intentionally ASYMMETRIC (min>=-1 AND max<=1) — do NOT symmetrize.
func DetectScoreFieldStatistically(stats FieldStats, items []any) (bool, float64) {
	// STEP1 numeric-only.
	if stats.FieldType != "numeric" {
		return false, 0.0
	}
	// STEP2 both bounds must be present.
	if stats.MinVal == nil || stats.MaxVal == nil {
		return false, 0.0
	}
	minVal := *stats.MinVal
	maxVal := *stats.MaxVal

	// STEP3 range bonus: first-match-wins if/else-if chain.
	confidence := 0.0
	isBounded := false
	switch {
	case minVal >= 0.0 && maxVal <= 1.0:
		confidence += 0.4
		isBounded = true
	case minVal >= 0.0 && maxVal <= 10.0:
		confidence += 0.3
		isBounded = true
	case minVal >= 0.0 && maxVal <= 100.0:
		confidence += 0.25
		isBounded = true
	case minVal >= -1.0 && maxVal <= 1.0:
		// ASYMMETRIC: lower checks minVal only, upper maxVal only.
		confidence += 0.35
		isBounded = true
	}

	// STEP4 not bounded -> reject.
	if !isBounded {
		return false, 0.0
	}

	// STEP5 sequential rejection: sample the first up-to-50 field values (raw, in
	// order) from objects containing the field, then reject if they form a
	// sequence. Missing/non-object elements are skipped.
	sampleSeq := make([]any, 0, scoreSampleSeqSize)
	for _, item := range items {
		if len(sampleSeq) >= scoreSampleSeqSize {
			break
		}
		if om, ok := item.(*orderedmap.OrderedMap); ok {
			if v, present := om.Get(stats.Name); present {
				sampleSeq = append(sampleSeq, v)
			}
		}
	}
	if DetectSequentialPattern(sampleSeq, true) {
		return false, 0.0
	}

	// STEP6 descending bonus: over ALL items, coerce the field value to a finite
	// f64 in order; if >=5 values and >70% of adjacent pairs are non-ascending,
	// add +0.3.
	valuesInOrder := make([]float64, 0, len(items))
	for _, item := range items {
		if om, ok := item.(*orderedmap.OrderedMap); ok {
			if v, present := om.Get(stats.Name); present {
				if f, ok := asFloat64(v); ok {
					valuesInOrder = append(valuesInOrder, f)
				}
			}
		}
	}
	if len(valuesInOrder) >= scoreDescendingMin {
		numPairs := len(valuesInOrder) - 1
		descendingCount := 0
		for i := 0; i < numPairs; i++ {
			if valuesInOrder[i] >= valuesInOrder[i+1] {
				descendingCount++
			}
		}
		if numPairs > 0 && float64(descendingCount)/float64(numPairs) > scoreDescendingFrac {
			confidence += 0.3
		}
	}

	// STEP7 float-fraction bonus: over the first up-to-20 finite values, count
	// non-integers; if >30% are non-integer, add +0.1.
	first20Limit := scoreFloatSampleSize
	if len(valuesInOrder) < first20Limit {
		first20Limit = len(valuesInOrder)
	}
	first20 := valuesInOrder[:first20Limit]
	if len(first20) > 0 {
		floatCount := 0
		for _, v := range first20 {
			if v != math.Trunc(v) {
				floatCount++
			}
		}
		if float64(floatCount) > float64(len(first20))*scoreFloatFraction {
			confidence += 0.1
		}
	}

	// STEP8 decide and cap.
	isScore := confidence >= scoreDecisionThresh
	boundedConfidence := math.Min(confidence, scoreConfidenceCap)
	return isScore, boundedConfidence
}

// asFloat64 coerces a decoded JSON value to a finite f64 using Rust as_f64
// semantics: JSON numbers (json.Number) yes, strings and bools NO. NaN/Inf are
// rejected. It returns (0, false) for anything non-coercible or non-finite.
func asFloat64(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil {
		return 0, false
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}
