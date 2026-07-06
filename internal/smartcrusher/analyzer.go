package smartcrusher

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// The SmartAnalyzer is the stateless statistical brain [ref: analyzer.rs]. Its
// eight methods decide WHETHER and HOW to crush an array; they emit NO output
// text and NO CCR markers (verdicts only). Field/key iteration is ASCII-SORTED
// everywhere (Go map range order is BANNED here — it would be non-deterministic
// and diverge from the upstream BTreeMap ordering). Deps ported first:
// DetectIDFieldStatistically / DetectScoreFieldStatistically (fielddetect.go),
// DetectStructuralOutliers / DetectErrorItemsForPreservation (outliers.go),
// the stats-math helpers (statsmath.go), and cardinalityRepr below.

// Analyzer gates and confidences [ref: (d) ANALYZER CRUSHABILITY].
const (
	changePointWindow = 5   // min series len = window*2 = 10; dedup gap cp-last > window.
	temporalSampleN   = 10  // ISO-datetime sampling: first 10 string values.
	topValuesN        = 5   // top_n_by_count keeps at most 5 entries.
	idConfidenceGate  = 0.7 // has_id_field requires best id confidence >= this.
	scorePatternGate  = 0.5 // detect_pattern search_results gate (STEP2 crushability has NONE).

	case0NonIDUniqueness = 0.1 // CASE0: non_id_content_uniqueness < this (+ has_id_field).
	case1MaxUniqueness   = 0.3 // CASE1: max_uniqueness < this.
	case23MaxUniqueness  = 0.8 // CASE2/3: max_uniqueness > this.

	logsMessageUniqueness = 0.5  // logs message_like: unique_ratio > this.
	logsMessageAvgLength  = 20.0 // logs message_like: avg_length > this.
	logsLevelUniqueness   = 0.1  // logs level_like: unique_ratio < this.
	logsLevelCardMin      = 2    // logs level_like: unique_count in [2,10].
	logsLevelCardMax      = 10
	logsClusterUniqueness = 0.5 // ClusterSample gate: 'message' field unique_ratio < this.

	unixSecondsLo = 1e9 // temporal numeric range (min_val only), inclusive.
	unixSecondsHi = 2e9
	unixMillisLo  = 1e12
	unixMillisHi  = 2e12

	// Confidences for the six decision cases, in order.
	case0Confidence = 0.85
	case1Confidence = 0.9
	case2Confidence = 0.85
	case3Confidence = 0.7
	case4Confidence = 0.6
	case5Confidence = 0.5

	// estimate_reduction bases by strategy and the constant-ratio bonus/cap.
	reductionBaseTimeSeries    = 0.7
	reductionBaseClusterSample = 0.8
	reductionBaseTopN          = 0.6
	reductionBaseSmartSample   = 0.5
	reductionBaseOther         = 0.3
	reductionConstantBonus     = 0.2
	reductionCap               = 0.95
)

// SmartAnalyzer is a stateless verdict engine carrying only its config.
type SmartAnalyzer struct {
	config Config
}

// NewSmartAnalyzer builds an analyzer bound to config. Only VarianceThreshold and
// MinItemsToAnalyze are consulted here.
func NewSmartAnalyzer(config Config) *SmartAnalyzer {
	return &SmartAnalyzer{config: config}
}

// AnalyzeArray is the top entry point [ref: analyzer.rs AnalyzeArray]. It guards
// empty/first-not-object arrays into a None-strategy, nil-crushability result
// (generic pattern), otherwise builds the sorted-key FieldStats map, detects the
// pattern, factors constant fields, runs the crushability tree, selects a
// strategy, and estimates the reduction.
func (a *SmartAnalyzer) AnalyzeArray(items []*orderedmap.OrderedMap) ArrayAnalysis {
	// (a) first-is-dict guard: non-empty AND items[0] is an object (non-nil).
	firstIsDict := len(items) > 0 && items[0] != nil
	if !firstIsDict {
		return ArrayAnalysis{
			ItemCount:           len(items),
			FieldStats:          orderedmap.New(),
			DetectedPattern:     "generic",
			RecommendedStrategy: StrategyNone,
			ConstantFields:      orderedmap.New(),
			EstimatedReduction:  0.0,
			Crushability:        nil,
		}
	}

	// (b) field_stats over the sorted union of all keys.
	allKeys := a.collectSortedKeys(items)
	fieldStats := orderedmap.New()
	for _, key := range allKeys {
		fieldStats.Set(key, a.AnalyzeField(key, items))
	}

	// (c) pattern.
	pattern := a.DetectPattern(fieldStats, items)

	// (d) constant_fields: sorted (k -> constant_value) for constant fields with a value.
	constantFields := orderedmap.New()
	for _, key := range allKeys {
		v, _ := fieldStats.Get(key)
		fs := v.(FieldStats)
		if fs.IsConstant && fs.ConstantValue != nil {
			constantFields.Set(key, *fs.ConstantValue)
		}
	}

	// (e) crushability.
	crushability := a.AnalyzeCrushability(items, fieldStats)

	// (f) strategy.
	strategy := a.SelectStrategy(fieldStats, pattern, len(items), &crushability)

	// (g) reduction (Skip -> 0.0).
	reduction := 0.0
	if strategy != StrategySkip {
		reduction = a.EstimateReduction(fieldStats, strategy, len(items))
	}

	c := crushability
	return ArrayAnalysis{
		ItemCount:           len(items),
		FieldStats:          fieldStats,
		DetectedPattern:     pattern,
		RecommendedStrategy: strategy,
		ConstantFields:      constantFields,
		EstimatedReduction:  reduction,
		Crushability:        &c,
	}
}

// AnalyzeField computes per-field statistics [ref: analyzer.rs AnalyzeField].
// Missing keys and explicit nulls both count as null values.
func (a *SmartAnalyzer) AnalyzeField(key string, items []*orderedmap.OrderedMap) FieldStats {
	// (a) values: obj.get(key), missing-or-null both -> nil.
	values := make([]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			values = append(values, nil)
			continue
		}
		v, ok := item.Get(key)
		if !ok {
			values = append(values, nil)
			continue
		}
		values = append(values, v)
	}

	// (b) non_null.
	nonNull := make([]any, 0, len(values))
	for _, v := range values {
		if v != nil {
			nonNull = append(nonNull, v)
		}
	}

	// (c) all-null field.
	if len(nonNull) == 0 {
		return FieldStats{
			Name:          key,
			FieldType:     "null",
			Count:         len(values),
			UniqueCount:   0,
			UniqueRatio:   0.0,
			IsConstant:    true,
			ConstantValue: nil,
			ChangePoints:  []int{},
			TopValues:     []TopValue{},
		}
	}

	fs := FieldStats{
		Name:         key,
		Count:        len(values),
		ChangePoints: []int{},
		TopValues:    []TopValue{},
	}

	// (d) field_type from non_null[0] (Bool checked BEFORE Number).
	fs.FieldType = fieldTypeOf(nonNull[0])

	// (e) uniqueness over ALL values (nulls included) via cardinalityRepr.
	seen := map[string]struct{}{}
	for _, v := range values {
		seen[cardinalityRepr(v)] = struct{}{}
	}
	fs.UniqueCount = len(seen)
	if len(values) == 0 {
		fs.UniqueRatio = 0.0
	} else {
		fs.UniqueRatio = float64(fs.UniqueCount) / float64(len(values))
	}
	fs.IsConstant = fs.UniqueCount == 1
	if fs.IsConstant {
		cv := nonNull[0]
		fs.ConstantValue = &cv
	}

	// (f) numeric stats over finite coercions of non_null.
	nums := make([]float64, 0, len(nonNull))
	for _, v := range nonNull {
		if f, ok := asFloat64(v); ok {
			nums = append(nums, f)
		}
	}
	if len(nums) > 0 {
		minV := nums[0]
		maxV := nums[0]
		for _, f := range nums[1:] {
			if f < minV {
				minV = f
			}
			if f > maxV {
				maxV = f
			}
		}
		meanV, meanOK := mean(nums)
		varV := 0.0
		if len(nums) > 1 {
			if v, ok := sampleVariance(nums); ok {
				varV = v
			}
		}
		allFinite := meanOK && isFinite(varV) && isFinite(minV) && isFinite(maxV)
		if allFinite {
			fs.MinVal = &minV
			fs.MaxVal = &maxV
			fs.MeanVal = &meanV
			fs.Variance = &varV
			fs.ChangePoints = a.DetectChangePoints(nums, changePointWindow)
		} else {
			// Overflow reset: min/max/mean nil, variance Some(0.0), change_points empty.
			zero := 0.0
			fs.MinVal = nil
			fs.MaxVal = nil
			fs.MeanVal = nil
			fs.Variance = &zero
			fs.ChangePoints = []int{}
		}
	}

	// (g) string stats.
	if fs.FieldType == "string" {
		strs := make([]string, 0, len(nonNull))
		for _, v := range nonNull {
			if s, ok := v.(string); ok {
				strs = append(strs, s)
			}
		}
		if len(strs) > 0 {
			// avg_length uses RUNE counts (utf8) — a statistical feature, not byte math.
			lengths := make([]float64, len(strs))
			for i, s := range strs {
				lengths[i] = float64(len([]rune(s)))
			}
			if avg, ok := mean(lengths); ok {
				fs.AvgLength = &avg
			}
			fs.TopValues = topNByCount(strs, topValuesN)
		}
	}

	return fs
}

// DetectChangePoints returns the deduped, ascending indices where the local mean
// shifts by more than variance_threshold*stdev [ref: analyzer.rs
// DetectChangePoints]. Series shorter than window*2, or with non-positive stdev,
// yield no change points. Subtraction i-window is saturating.
func (a *SmartAnalyzer) DetectChangePoints(values []float64, window int) []int {
	// (a) too short.
	if len(values) < window*2 {
		return []int{}
	}
	// (b) overall_std must be present and positive.
	overallStd, ok := sampleStdev(values)
	if !ok || overallStd <= 0.0 {
		return []int{}
	}
	// (c) threshold.
	threshold := a.config.VarianceThreshold * overallStd

	// (d) scan window..len-window.
	raw := []int{}
	for i := window; i < len(values)-window; i++ {
		before := meanOrZero(values[i-window : i])
		after := meanOrZero(values[i : i+window])
		if math.Abs(after-before) > threshold {
			raw = append(raw, i)
		}
	}
	// (e) nothing raw.
	if len(raw) == 0 {
		return []int{}
	}
	// (f) greedy dedup: keep first, then any cp more than `window` past the last kept.
	deduped := []int{raw[0]}
	for _, cp := range raw[1:] {
		if cp-deduped[len(deduped)-1] > window {
			deduped = append(deduped, cp)
		}
	}
	return deduped
}

// DetectPattern classifies the array as time_series | logs | search_results |
// generic [ref: analyzer.rs DetectPattern]. Field iteration is ASCII-sorted.
func (a *SmartAnalyzer) DetectPattern(fieldStats *orderedmap.OrderedMap, items []*orderedmap.OrderedMap) string {
	keys := sortedOrderedKeys(fieldStats)

	// (a)/(b) temporal + numeric-variance signals.
	hasTimestamp := a.DetectTemporalField(fieldStats, items)
	hasNumericWithVariance := false
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType == "numeric" && fs.Variance != nil && *fs.Variance > 0.0 {
			hasNumericWithVariance = true
			break
		}
	}
	// (c) both -> time_series.
	if hasTimestamp && hasNumericWithVariance {
		return "time_series"
	}

	// (d) log signals over string fields.
	messageLike := false
	levelLike := false
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType != "string" {
			continue
		}
		avgLen := 0.0
		if fs.AvgLength != nil {
			avgLen = *fs.AvgLength
		}
		if fs.UniqueRatio > logsMessageUniqueness && avgLen > logsMessageAvgLength {
			messageLike = true
		}
		if fs.UniqueRatio < logsLevelUniqueness && fs.UniqueCount >= logsLevelCardMin && fs.UniqueCount <= logsLevelCardMax {
			levelLike = true
		}
	}
	if messageLike && levelLike {
		return "logs"
	}

	// (e) score field (with a >= 0.5 confidence gate — unlike crushability STEP2).
	anyItems := toAnySlice(items)
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		isScore, conf := DetectScoreFieldStatistically(fs, anyItems)
		if isScore && conf >= scorePatternGate {
			return "search_results"
		}
	}

	// (f) generic.
	return "generic"
}

// DetectTemporalField reports whether any field looks like a timestamp
// [ref: analyzer.rs DetectTemporalField]. String fields sample the first 10 items
// and check the ISO-datetime/date fraction over the SAMPLED string count (not the
// full array length); numeric fields check ONLY min_val against the
// unix-seconds/millis ranges (max_val must merely be present).
func (a *SmartAnalyzer) DetectTemporalField(fieldStats *orderedmap.OrderedMap, items []*orderedmap.OrderedMap) bool {
	keys := sortedOrderedKeys(fieldStats)
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType == "string" {
			// (a) sample the first 10 items' string value on this key. The ISO
			// fraction divides by the number of STRING values actually sampled
			// (capped at temporalSampleN), NOT the full array length — mirrors the
			// upstream sample-length divisor and the Task 5 strSample convention
			// in DetectIDFieldStatistically. Dividing by len(items) would silently
			// defeat the time_series pattern for any array > 20 items whose first
			// 10 values are ISO datetimes (10/N < 0.5).
			isoCount := 0
			sampledStringCount := 0
			limit := temporalSampleN
			if len(items) < limit {
				limit = len(items)
			}
			for i := 0; i < limit; i++ {
				if items[i] == nil {
					continue
				}
				v, ok := items[i].Get(k)
				if !ok {
					continue
				}
				s, ok := v.(string)
				if !ok {
					continue
				}
				sampledStringCount++
				if isISODateTime(s) || isISODate(s) {
					isoCount++
				}
			}
			if sampledStringCount > 0 && float64(isoCount)/float64(sampledStringCount) > 0.5 {
				return true
			}
		} else if fs.FieldType == "numeric" {
			// (b) numeric: check ONLY min_val range (max_val must merely be present).
			if fs.MinVal != nil && fs.MaxVal != nil {
				mn := *fs.MinVal
				if (mn >= unixSecondsLo && mn <= unixSecondsHi) || (mn >= unixMillisLo && mn <= unixMillisHi) {
					return true
				}
			}
		}
	}
	// (c) else false.
	return false
}

// AnalyzeCrushability is the six-case decision tree [ref: analyzer.rs
// AnalyzeCrushability]. Field iteration is ASCII-sorted throughout; case order is
// load-bearing (CASE0 precedes CASE1). It gathers signals (id/score/outliers/
// error/anomalies/change_points), computes uniqueness aggregates, then returns the
// first matching verdict.
func (a *SmartAnalyzer) AnalyzeCrushability(items []*orderedmap.OrderedMap, fieldStats *orderedmap.OrderedMap) CrushabilityAnalysis {
	keys := sortedOrderedKeys(fieldStats)
	anyItems := toAnySlice(items)

	signalsPresent := []string{}
	signalsAbsent := []string{}

	// STEP1 ID (highest-confidence wins).
	idFieldName := ""
	idUniqueness := 0.0
	idConfidence := 0.0
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		fieldValues := fieldValuesFor(items, k)
		isID, conf := DetectIDFieldStatistically(fs, fieldValues)
		if isID && conf > idConfidence {
			idConfidence = conf
			idFieldName = k
			idUniqueness = fs.UniqueRatio
		}
	}
	hasIDField := idFieldName != "" && idConfidence >= idConfidenceGate

	// STEP2 score (first match, NO confidence gate).
	hasScoreField := false
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		isScore, conf := DetectScoreFieldStatistically(fs, anyItems)
		if isScore {
			hasScoreField = true
			signalsPresent = append(signalsPresent, fmt.Sprintf("score_field:%s(conf=%.2f)", k, conf))
			break
		}
	}
	if !hasScoreField {
		signalsAbsent = append(signalsAbsent, "score_field")
	}

	// STEP3 structural outliers.
	outlierIndices := DetectStructuralOutliers(items)
	structuralOutlierCount := len(outlierIndices)
	if structuralOutlierCount > 0 {
		signalsPresent = append(signalsPresent, fmt.Sprintf("structural_outliers:%d", structuralOutlierCount))
	} else {
		signalsAbsent = append(signalsAbsent, "structural_outliers")
	}

	// STEP3b error fallback (only when there were no structural outliers).
	errorKeywordIndices := DetectErrorItemsForPreservation(items, nil)
	keywordErrorCount := len(errorKeywordIndices)
	if keywordErrorCount > 0 && structuralOutlierCount == 0 {
		signalsPresent = append(signalsPresent, fmt.Sprintf("error_keywords:%d", keywordErrorCount))
	}
	errorCount := structuralOutlierCount
	if keywordErrorCount > errorCount {
		errorCount = keywordErrorCount
	}

	// STEP4 anomalies (mean ± variance_threshold*stdev per numeric field).
	anomalies := map[int]struct{}{}
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType != "numeric" || fs.MeanVal == nil || fs.Variance == nil || *fs.Variance <= 0.0 {
			continue
		}
		std := math.Sqrt(*fs.Variance)
		if std <= 0.0 {
			continue
		}
		threshold := a.config.VarianceThreshold * std
		meanV := *fs.MeanVal
		for i, item := range items {
			if item == nil {
				continue
			}
			v, ok := item.Get(k)
			if !ok {
				continue
			}
			num, ok := asFloat64(v)
			if !ok || math.IsNaN(num) {
				continue
			}
			if math.Abs(num-meanV) > threshold {
				anomalies[i] = struct{}{}
			}
		}
	}
	anomalyCount := len(anomalies)
	if anomalyCount > 0 {
		signalsPresent = append(signalsPresent, "anomalies")
	} else {
		signalsAbsent = append(signalsAbsent, "anomalies")
	}

	// STEP5 uniqueness aggregates.
	stringRatios := []float64{}
	nonIDNumericRatios := []float64{}
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType == "string" && k != idFieldName {
			stringRatios = append(stringRatios, fs.UniqueRatio)
		}
		if fs.FieldType == "numeric" && k != idFieldName {
			nonIDNumericRatios = append(nonIDNumericRatios, fs.UniqueRatio)
		}
	}
	avgStringUniqueness := meanOrZero(stringRatios)
	avgNonIDNumericUniqueness := meanOrZero(nonIDNumericRatios)
	maxUniqueness := math.Max(math.Max(avgStringUniqueness, idUniqueness), 0.0)
	nonIDContentUniqueness := math.Max(avgStringUniqueness, avgNonIDNumericUniqueness)

	// STEP6 change points.
	hasChangePoints := false
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.FieldType == "numeric" && len(fs.ChangePoints) > 0 {
			hasChangePoints = true
			break
		}
	}
	if hasChangePoints {
		signalsPresent = append(signalsPresent, "change_points")
	}
	hasAnySignal := len(signalsPresent) > 0

	// Shared metrics attached to every verdict.
	base := CrushabilityAnalysis{
		HasIDField:          hasIDField,
		IDUniqueness:        idUniqueness,
		AvgStringUniqueness: avgStringUniqueness,
		HasScoreField:       hasScoreField,
		ErrorItemCount:      errorCount,
		AnomalyCount:        anomalyCount,
		SignalsPresent:      signalsPresent,
		SignalsAbsent:       signalsAbsent,
	}

	// DECISION (first match wins).
	switch {
	case nonIDContentUniqueness < case0NonIDUniqueness && hasIDField:
		base.Crushable = true
		base.Confidence = case0Confidence
		base.Reason = "repetitive_content_with_ids"
		base.SignalsPresent = append(base.SignalsPresent, "repetitive_content")
	case maxUniqueness < case1MaxUniqueness:
		base.Crushable = true
		base.Confidence = case1Confidence
		base.Reason = "low_uniqueness_safe_to_sample"
	case hasIDField && maxUniqueness > case23MaxUniqueness && !hasAnySignal:
		base.Crushable = false
		base.Confidence = case2Confidence
		base.Reason = "unique_entities_no_signal"
	case maxUniqueness > case23MaxUniqueness && hasAnySignal:
		base.Crushable = true
		base.Confidence = case3Confidence
		base.Reason = "unique_entities_with_signal"
	case !hasAnySignal:
		base.Crushable = false
		base.Confidence = case4Confidence
		base.Reason = "medium_uniqueness_no_signal"
	default:
		base.Crushable = true
		base.Confidence = case5Confidence
		base.Reason = "medium_uniqueness_with_signal"
	}
	return base
}

// SelectStrategy maps the crushability verdict + pattern to a CompressionStrategy
// [ref: analyzer.rs SelectStrategy]. TimeSeries/ClusterSample/TopN are computed for
// parity even though their crushers are DEFERRED; downstream falls back.
func (a *SmartAnalyzer) SelectStrategy(fieldStats *orderedmap.OrderedMap, pattern string, itemCount int, crushability *CrushabilityAnalysis) CompressionStrategy {
	// (a) too few items.
	if itemCount < a.config.MinItemsToAnalyze {
		return StrategyNone
	}
	// (b) not crushable -> skip.
	if crushability != nil && !crushability.Crushable {
		return StrategySkip
	}

	keys := sortedOrderedKeys(fieldStats)

	// (c) time_series with any numeric change points.
	if pattern == "time_series" {
		for _, k := range keys {
			fs := getFieldStats(fieldStats, k)
			if fs.FieldType == "numeric" && len(fs.ChangePoints) > 0 {
				return StrategyTimeSeries
			}
		}
	}

	// (d) logs: first sorted field whose lowercased key contains "message" with
	// unique_ratio < 0.5 -> ClusterSample.
	if pattern == "logs" {
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), "message") {
				fs := getFieldStats(fieldStats, k)
				if fs.UniqueRatio < logsClusterUniqueness {
					return StrategyClusterSample
				}
				break
			}
		}
	}

	// (e) search_results -> TopN.
	if pattern == "search_results" {
		return StrategyTopN
	}

	// (f) else SmartSample.
	return StrategySmartSample
}

// EstimateReduction estimates the fractional size reduction [ref: analyzer.rs
// EstimateReduction]. itemCount is unused (kept for signature parity). None
// strategy or empty field_stats yield 0.0 (the empty guard also prevents an
// upstream ZeroDivisionError).
func (a *SmartAnalyzer) EstimateReduction(fieldStats *orderedmap.OrderedMap, strategy CompressionStrategy, itemCount int) float64 {
	_ = itemCount
	if strategy == StrategyNone {
		return 0.0
	}
	keys := fieldStats.Keys()
	if len(keys) == 0 {
		return 0.0
	}
	constantCount := 0
	for _, k := range keys {
		fs := getFieldStats(fieldStats, k)
		if fs.IsConstant {
			constantCount++
		}
	}
	constantRatio := float64(constantCount) / float64(len(keys))

	var base float64
	switch strategy {
	case StrategyTimeSeries:
		base = reductionBaseTimeSeries
	case StrategyClusterSample:
		base = reductionBaseClusterSample
	case StrategyTopN:
		base = reductionBaseTopN
	case StrategySmartSample:
		base = reductionBaseSmartSample
	default:
		base = reductionBaseOther
	}
	return math.Min(base+constantRatio*reductionConstantBonus, reductionCap)
}

// collectSortedKeys returns the ASCII-sorted union of keys across all object items.
func (a *SmartAnalyzer) collectSortedKeys(items []*orderedmap.OrderedMap) []string {
	set := map[string]struct{}{}
	for _, item := range items {
		if item == nil {
			continue
		}
		for _, k := range item.Keys() {
			set[k] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// fieldTypeOf maps a decoded JSON value to a field_type label. Bool is checked
// BEFORE Number (Go's type switch already separates them, but the ordering mirrors
// the upstream isinstance chain). Domain: numeric|string|boolean|object|array|null.
func fieldTypeOf(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case json.Number:
		return "numeric"
	case string:
		return "string"
	case *orderedmap.OrderedMap:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

// cardinalityRepr renders value as a cardinality/uniqueness KEY only
// [ref: analyzer.rs python_repr — the cardinality-counting one]. Rendering:
// Null->"None", true->"True", false->"False", Number->its JSON token, String->its
// raw text (NOT quoted), Object/Array->compact JSON (json.Marshal).
//
// This is intentionally NOT anchors.go's pythonRepr: that helper single-quotes
// strings and renders objects Python-style ({'k': v}). The two unexported helpers
// live in the SAME package and MUST keep distinct names (naming both pythonRepr is
// a redeclaration compile error). cardinalityRepr is never substring-matched
// against anchors — it exists solely to dedup field values for unique_count.
func cardinalityRepr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case json.Number:
		return v.String()
	case string:
		return v
	default:
		// Arrays ([]any) and objects (*orderedmap.OrderedMap) -> compact JSON.
		b, err := json.Marshal(v)
		if err != nil {
			return "None"
		}
		return string(b)
	}
}

// topNByCount counts occurrences of each string, sorts by count DESC with ties
// broken by first-occurrence (stable), and returns up to n entries.
func topNByCount(strs []string, n int) []TopValue {
	counts := map[string]int{}
	order := []string{}
	for _, s := range strs {
		if _, seen := counts[s]; !seen {
			order = append(order, s)
		}
		counts[s]++
	}
	entries := make([]TopValue, len(order))
	for i, s := range order {
		entries[i] = TopValue{Value: s, Count: counts[s]}
	}
	// Stable sort by count DESC preserves first-occurrence order for ties.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Count > entries[j].Count
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	return entries
}

// isISODateTime reports whether s is at least 19 bytes and its first 19 bytes match
// YYYY-MM-DD[T or space]HH:MM:SS. Trailing bytes (fractional seconds, zone) are
// ignored [ref: analyzer.rs is_iso_datetime].
func isISODateTime(s string) bool {
	if len(s) < 19 {
		return false
	}
	b := s[:19]
	return isDigit(b[0]) && isDigit(b[1]) && isDigit(b[2]) && isDigit(b[3]) &&
		b[4] == '-' && isDigit(b[5]) && isDigit(b[6]) &&
		b[7] == '-' && isDigit(b[8]) && isDigit(b[9]) &&
		(b[10] == 'T' || b[10] == ' ') &&
		isDigit(b[11]) && isDigit(b[12]) &&
		b[13] == ':' && isDigit(b[14]) && isDigit(b[15]) &&
		b[16] == ':' && isDigit(b[17]) && isDigit(b[18])
}

// isISODate reports whether s is EXACTLY 10 bytes matching YYYY-MM-DD
// [ref: analyzer.rs is_iso_date].
func isISODate(s string) bool {
	if len(s) != 10 {
		return false
	}
	return isDigit(s[0]) && isDigit(s[1]) && isDigit(s[2]) && isDigit(s[3]) &&
		s[4] == '-' && isDigit(s[5]) && isDigit(s[6]) &&
		s[7] == '-' && isDigit(s[8]) && isDigit(s[9])
}

// isDigit reports whether b is an ASCII 0-9.
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// sortedKeys returns the ASCII-sorted keys of a set.
func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedOrderedKeys returns the ASCII-sorted keys of an orderedmap (the analyzer
// iterates fields in sorted order, NOT insertion order — mirrors BTreeMap).
func sortedOrderedKeys(om *orderedmap.OrderedMap) []string {
	keys := append([]string(nil), om.Keys()...)
	sort.Strings(keys)
	return keys
}

// getFieldStats fetches the FieldStats stored under key (present by construction).
func getFieldStats(om *orderedmap.OrderedMap, key string) FieldStats {
	v, _ := om.Get(key)
	return v.(FieldStats)
}

// fieldValuesFor extracts the per-item value on key (missing/null -> nil) as []any,
// the shape DetectIDFieldStatistically consumes.
func fieldValuesFor(items []*orderedmap.OrderedMap, key string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			out = append(out, nil)
			continue
		}
		v, ok := item.Get(key)
		if !ok {
			out = append(out, nil)
			continue
		}
		out = append(out, v)
	}
	return out
}

// toAnySlice widens the object-item slice to []any for DetectScoreFieldStatistically.
func toAnySlice(items []*orderedmap.OrderedMap) []any {
	out := make([]any, len(items))
	for i, item := range items {
		if item == nil {
			out[i] = nil
			continue
		}
		out[i] = item
	}
	return out
}

// meanOrZero returns the arithmetic mean, or 0.0 for empty input (mirrors the
// upstream mean().unwrap_or(0.0) usage inside change-point and aggregate math).
func meanOrZero(values []float64) float64 {
	if m, ok := mean(values); ok {
		return m
	}
	return 0.0
}
