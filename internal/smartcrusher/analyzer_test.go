package smartcrusher

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// -- cardinalityRepr (cardinality/uniqueness key stringifier) --
//
// It MUST render objects/arrays as JSON ({"a":1}), NOT the Python single-quote
// form ({'a': 1}) that anchors.go's pythonRepr uses. This test pins the two
// unexported helpers as intentionally distinct.
func TestCardinalityReprDistinctFromPythonRepr(t *testing.T) {
	om := orderedmap.New()
	v, err := decodeJSON(`1`)
	if err != nil {
		t.Fatalf("decodeJSON error: %v", err)
	}
	om.Set("a", v)

	got := cardinalityRepr(om)
	if got != `{"a":1}` {
		t.Fatalf("cardinalityRepr(object) = %q, want %q (JSON form)", got, `{"a":1}`)
	}
	// pythonRepr renders the SAME object with single quotes -> proves distinctness.
	if pr := pythonRepr(om); pr == got {
		t.Fatalf("cardinalityRepr and pythonRepr must differ on objects; both = %q", got)
	}
}

func TestCardinalityReprScalars(t *testing.T) {
	num, _ := decodeJSON(`42`)
	arr, _ := decodeJSON(`[1, 2]`)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"null", nil, "None"},
		{"true", true, "True"},
		{"false", false, "False"},
		{"number", num, "42"},
		{"string raw not quoted", "hello", "hello"},
		{"array json", arr, "[1,2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cardinalityRepr(tc.in); got != tc.want {
				t.Fatalf("cardinalityRepr(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// -- AnalyzeArray guards --

func TestAnalyzeArrayEmpty(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	got := a.AnalyzeArray([]*orderedmap.OrderedMap{})
	if got.ItemCount != 0 {
		t.Fatalf("ItemCount = %d, want 0", got.ItemCount)
	}
	if got.RecommendedStrategy != StrategyNone {
		t.Fatalf("strategy = %v, want None", got.RecommendedStrategy)
	}
	if got.DetectedPattern != "generic" {
		t.Fatalf("pattern = %q, want generic", got.DetectedPattern)
	}
	if got.Crushability != nil {
		t.Fatalf("Crushability = %+v, want nil", got.Crushability)
	}
	if got.EstimatedReduction != 0.0 {
		t.Fatalf("reduction = %v, want 0", got.EstimatedReduction)
	}
}

func TestAnalyzeArrayFirstNotObject(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	// First element is a null pointer (non-object) -> None strategy, nil crushability.
	items := mustDecodeObjects(t, `[null, {"a": 1}]`)
	got := a.AnalyzeArray(items)
	if got.RecommendedStrategy != StrategyNone {
		t.Fatalf("strategy = %v, want None", got.RecommendedStrategy)
	}
	if got.Crushability != nil {
		t.Fatalf("Crushability = %+v, want nil", got.Crushability)
	}
	if got.DetectedPattern != "generic" {
		t.Fatalf("pattern = %q, want generic", got.DetectedPattern)
	}
	if got.ItemCount != 2 {
		t.Fatalf("ItemCount = %d, want 2 (len of whole array)", got.ItemCount)
	}
}

// -- AnalyzeField --

func TestAnalyzeFieldAllNull(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	// key "x" is null in one item and MISSING in the other -> both count as null.
	items := mustDecodeObjects(t, `[{"x": null}, {"y": 1}]`)
	fs := a.AnalyzeField("x", items)
	if fs.FieldType != "null" {
		t.Fatalf("field_type = %q, want null", fs.FieldType)
	}
	if !fs.IsConstant {
		t.Fatalf("is_constant = false, want true (all null)")
	}
	if fs.UniqueCount != 0 {
		t.Fatalf("unique_count = %d, want 0", fs.UniqueCount)
	}
	if fs.UniqueRatio != 0.0 {
		t.Fatalf("unique_ratio = %v, want 0", fs.UniqueRatio)
	}
	if fs.ConstantValue != nil {
		t.Fatalf("constant_value = %v, want nil", fs.ConstantValue)
	}
}

// Missing key vs explicit null are BOTH stringified to "None" via cardinalityRepr
// and counted in cardinality: two objects, one with x=1 and one missing x, give
// distinct values {1, None} -> unique_count 2.
func TestAnalyzeFieldMissingEqualsNullInCardinality(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[{"x": 1}, {"y": 2}]`)
	fs := a.AnalyzeField("x", items)
	if fs.Count != 2 {
		t.Fatalf("count = %d, want 2", fs.Count)
	}
	if fs.UniqueCount != 2 {
		t.Fatalf("unique_count = %d, want 2 ({1, None})", fs.UniqueCount)
	}
	if fs.FieldType != "numeric" {
		t.Fatalf("field_type = %q, want numeric (non-null[0] is a number)", fs.FieldType)
	}
}

// Numeric field with a single value: variance n<2 -> Some(0.0), NOT nil.
func TestAnalyzeFieldNumericVarianceSingle(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[{"n": 5}]`)
	fs := a.AnalyzeField("n", items)
	if fs.Variance == nil {
		t.Fatalf("variance = nil, want Some(0.0)")
	}
	if *fs.Variance != 0.0 {
		t.Fatalf("variance = %v, want 0.0", *fs.Variance)
	}
	if fs.MeanVal == nil || *fs.MeanVal != 5.0 {
		t.Fatalf("mean = %v, want 5.0", fs.MeanVal)
	}
}

// Numeric overflow -> ALL-OR-NOTHING reset: min/max/mean nil, variance 0.0 (NOT
// nil), change_points empty.
func TestAnalyzeFieldNumericOverflowReset(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	// Two 1e308 values -> mean overflows to +Inf -> not-all-finite -> reset.
	items := mustDecodeObjects(t, `[{"n": 1e308}, {"n": 1e308}]`)
	fs := a.AnalyzeField("n", items)
	if fs.MinVal != nil || fs.MaxVal != nil || fs.MeanVal != nil {
		t.Fatalf("overflow reset: min/max/mean should be nil, got %v/%v/%v", fs.MinVal, fs.MaxVal, fs.MeanVal)
	}
	if fs.Variance == nil || *fs.Variance != 0.0 {
		t.Fatalf("overflow reset: variance should be Some(0.0), got %v", fs.Variance)
	}
	if len(fs.ChangePoints) != 0 {
		t.Fatalf("overflow reset: change_points should be empty, got %v", fs.ChangePoints)
	}
}

// String field: avg_length is the mean of per-string RUNE counts; top_values are
// frequency-descending.
func TestAnalyzeFieldStringAvgLengthRunes(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	// "café" is 4 runes but 5 bytes; "ab" is 2 runes. mean = (4+2)/2 = 3.0.
	items := mustDecodeObjects(t, `[{"s": "café"}, {"s": "ab"}]`)
	fs := a.AnalyzeField("s", items)
	if fs.FieldType != "string" {
		t.Fatalf("field_type = %q, want string", fs.FieldType)
	}
	if fs.AvgLength == nil {
		t.Fatalf("avg_length = nil, want Some(3.0)")
	}
	if math.Abs(*fs.AvgLength-3.0) > 1e-9 {
		t.Fatalf("avg_length = %v, want 3.0 (rune count)", *fs.AvgLength)
	}
}

func TestAnalyzeFieldBoolCheckedBeforeNumber(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[{"b": true}, {"b": false}]`)
	fs := a.AnalyzeField("b", items)
	if fs.FieldType != "boolean" {
		t.Fatalf("field_type = %q, want boolean", fs.FieldType)
	}
}

// -- DetectChangePoints --

func TestDetectChangePointsTooShort(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9} // len 9 < 10
	if cps := a.DetectChangePoints(vals, 5); len(cps) != 0 {
		t.Fatalf("len<10 should give empty, got %v", cps)
	}
}

func TestDetectChangePointsConstant(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 7.0
	}
	if cps := a.DetectChangePoints(vals, 5); len(cps) != 0 {
		t.Fatalf("constant series should give empty (stdev 0), got %v", cps)
	}
}

// A clear 3-segment step function fires at least one change point.
func TestDetectChangePointsThreeSegmentFires(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	vals := []float64{}
	for i := 0; i < 10; i++ {
		vals = append(vals, 0.0)
	}
	for i := 0; i < 10; i++ {
		vals = append(vals, 100.0)
	}
	for i := 0; i < 10; i++ {
		vals = append(vals, 0.0)
	}
	cps := a.DetectChangePoints(vals, 5)
	if len(cps) == 0 {
		t.Fatalf("3-segment step should fire >=1 change point, got none")
	}
	// Ascending, deduped by construction.
	for i := 1; i < len(cps); i++ {
		if cps[i] <= cps[i-1] {
			t.Fatalf("change points not strictly ascending: %v", cps)
		}
	}
}

// -- DetectTemporalField --

// The ISO fraction divides by the SAMPLED string count (capped at 10), NOT the
// full array length. For an array > 20 items whose first 10 timestamp values are
// ISO datetimes, upstream sees 10/10 = 1.0 > 0.5 (true). Dividing by len(items)
// (e.g. 10/30) would silently defeat detection.
func TestDetectTemporalFieldISOStringSampledDivisor(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		// All 30 items carry an ISO datetime on "ts" (first 10 alone already do).
		fmt.Fprintf(&b, `{"ts": "2026-06-%02dT12:00:00", "n": %d}`, (i%28)+1, i)
	}
	b.WriteString("]")
	items := mustDecodeObjects(t, b.String())
	fs := analyzeAllFields(a, items)
	if !a.DetectTemporalField(fs, items) {
		t.Fatalf("expected true: first 10 string values are ISO datetimes (10/10 > 0.5)")
	}
}

// Numeric temporal field: unix-seconds min_val in [1e9, 2e9] (with max_val present).
func TestDetectTemporalFieldNumericUnixSeconds(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[
		{"ts": 1600000000}, {"ts": 1600000001}, {"ts": 1600000002},
		{"ts": 1600000003}, {"ts": 1600000004}, {"ts": 1600000005}
	]`)
	fs := analyzeAllFields(a, items)
	if !a.DetectTemporalField(fs, items) {
		t.Fatalf("expected true: numeric min_val in unix-seconds range")
	}
}

// -- DetectPattern --

// time_series requires a temporal field AND a numeric field with variance > 0.
// A 30-item array whose first 10 "ts" values are ISO datetimes plus a varying
// numeric "n" classifies as time_series (the sampled-divisor fix is load-bearing:
// with the old len(items) divisor DetectTemporalField would return false and this
// would fall through to generic).
func TestDetectPatternTimeSeries(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"ts": "2026-06-%02dT12:00:00", "n": %d}`, (i%28)+1, i*3)
	}
	b.WriteString("]")
	items := mustDecodeObjects(t, b.String())
	fs := analyzeAllFields(a, items)
	if got := a.DetectPattern(fs, items); got != "time_series" {
		t.Fatalf("pattern = %q, want time_series", got)
	}
}

// -- AnalyzeCrushability decision tree --

// CASE0: repetitive content with ids -> crushable, confidence 0.85. A single-field
// UUID array has an id field but NO non-id content, so
// non_id_content_uniqueness == 0 < 0.1 and CASE0 fires BEFORE CASE1 (load-bearing
// ordering).
func TestCrushabilityRepetitiveContentWithIDs(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[
		{"id": "550e8400-e29b-41d4-a716-446655440000"},
		{"id": "550e8400-e29b-41d4-a716-446655440001"},
		{"id": "550e8400-e29b-41d4-a716-446655440002"},
		{"id": "550e8400-e29b-41d4-a716-446655440003"},
		{"id": "550e8400-e29b-41d4-a716-446655440004"},
		{"id": "550e8400-e29b-41d4-a716-446655440005"}
	]`)
	fs := analyzeAllFields(a, items)
	c := a.AnalyzeCrushability(items, fs)
	if !c.Crushable {
		t.Fatalf("expected crushable (CASE0)")
	}
	if math.Abs(c.Confidence-0.85) > 1e-9 {
		t.Fatalf("confidence = %v, want 0.85 (CASE0)", c.Confidence)
	}
	if c.Reason != "repetitive_content_with_ids" {
		t.Fatalf("reason = %q, want repetitive_content_with_ids", c.Reason)
	}
	// "repetitive_content" is appended to the present signals in CASE0.
	found := false
	for _, s := range c.SignalsPresent {
		if s == "repetitive_content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CASE0 should append repetitive_content signal, got %v", c.SignalsPresent)
	}
}

// CASE1: low uniqueness -> crushable, confidence 0.9.
func TestCrushabilityLowUniqueness(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	// Many items sharing one repeated string value on a non-id field.
	items := mustDecodeObjects(t, `[
		{"status": "ok"}, {"status": "ok"}, {"status": "ok"}, {"status": "ok"},
		{"status": "ok"}, {"status": "ok"}, {"status": "ok"}, {"status": "ok"}
	]`)
	fs := analyzeAllFields(a, items)
	c := a.AnalyzeCrushability(items, fs)
	if !c.Crushable {
		t.Fatalf("expected crushable")
	}
	if math.Abs(c.Confidence-0.9) > 1e-9 {
		t.Fatalf("confidence = %v, want 0.9 (CASE1)", c.Confidence)
	}
	if c.Reason != "low_uniqueness_safe_to_sample" {
		t.Fatalf("reason = %q, want low_uniqueness_safe_to_sample", c.Reason)
	}
}

// CASE2: an ID field with all-unique entities and NO other signal -> NOT crushable,
// confidence 0.85. The non-id "status" field (uniqueRatio 0.25) keeps
// non_id_content_uniqueness >= 0.1 (avoiding CASE0's repetitive-content branch)
// while its 4/4 split (a dominant value covers >=80% with topK<=5) fires no
// structural outliers, so has_any_signal stays false and maxUniqueness (from the
// UUID id's 1.0) exceeds 0.8.
func TestCrushabilityUniqueEntitiesNoSignal(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	items := mustDecodeObjects(t, `[
		{"id": "550e8400-e29b-41d4-a716-446655440000", "status": "active"},
		{"id": "550e8400-e29b-41d4-a716-446655440001", "status": "active"},
		{"id": "550e8400-e29b-41d4-a716-446655440002", "status": "active"},
		{"id": "550e8400-e29b-41d4-a716-446655440003", "status": "active"},
		{"id": "550e8400-e29b-41d4-a716-446655440004", "status": "idle"},
		{"id": "550e8400-e29b-41d4-a716-446655440005", "status": "idle"},
		{"id": "550e8400-e29b-41d4-a716-446655440006", "status": "idle"},
		{"id": "550e8400-e29b-41d4-a716-446655440007", "status": "idle"}
	]`)
	fs := analyzeAllFields(a, items)
	c := a.AnalyzeCrushability(items, fs)
	if c.Crushable {
		t.Fatalf("expected NOT crushable (unique entities, no signal)")
	}
	if math.Abs(c.Confidence-0.85) > 1e-9 {
		t.Fatalf("confidence = %v, want 0.85 (CASE2)", c.Confidence)
	}
	if c.Reason != "unique_entities_no_signal" {
		t.Fatalf("reason = %q, want unique_entities_no_signal", c.Reason)
	}
	if !c.HasIDField {
		t.Fatalf("HasIDField = false, want true")
	}
}

// -- SelectStrategy --

func TestSelectStrategyTooFewItems(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	c := SkipAnalysis("x", 0.5)
	c.Crushable = true
	got := a.SelectStrategy(orderedmap.New(), "generic", 4, &c)
	if got != StrategyNone {
		t.Fatalf("count<5 should give None, got %v", got)
	}
}

func TestSelectStrategyNotCrushableSkips(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	c := SkipAnalysis("x", 0.5) // Crushable=false
	got := a.SelectStrategy(orderedmap.New(), "generic", 10, &c)
	if got != StrategySkip {
		t.Fatalf("!crushable should give Skip, got %v", got)
	}
}

func TestSelectStrategyGenericSmartSample(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	c := SkipAnalysis("x", 0.5)
	c.Crushable = true
	got := a.SelectStrategy(orderedmap.New(), "generic", 10, &c)
	if got != StrategySmartSample {
		t.Fatalf("generic crushable should give SmartSample, got %v", got)
	}
}

// The logs branch fires ONLY for a field whose lowercased key contains "message"
// with unique_ratio < 0.5.
func TestSelectStrategyLogsMessageBranch(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	c := SkipAnalysis("x", 0.5)
	c.Crushable = true

	fs := orderedmap.New()
	msg := FieldStats{Name: "Message", FieldType: "string", UniqueRatio: 0.4}
	fs.Set("Message", msg) // key contains "message" case-insensitively
	got := a.SelectStrategy(fs, "logs", 10, &c)
	if got != StrategyClusterSample {
		t.Fatalf("logs + message field uniq<0.5 should give ClusterSample, got %v", got)
	}

	// Same pattern but the message field's uniqueness is >= 0.5 -> falls to SmartSample.
	fs2 := orderedmap.New()
	msg2 := FieldStats{Name: "message", FieldType: "string", UniqueRatio: 0.6}
	fs2.Set("message", msg2)
	got2 := a.SelectStrategy(fs2, "logs", 10, &c)
	if got2 != StrategySmartSample {
		t.Fatalf("logs + message field uniq>=0.5 should fall to SmartSample, got %v", got2)
	}

	// pattern==logs but NO message-named field -> SmartSample.
	fs3 := orderedmap.New()
	fs3.Set("level", FieldStats{Name: "level", FieldType: "string", UniqueRatio: 0.4})
	got3 := a.SelectStrategy(fs3, "logs", 10, &c)
	if got3 != StrategySmartSample {
		t.Fatalf("logs without message field should give SmartSample, got %v", got3)
	}
}

// -- EstimateReduction --

func TestEstimateReductionNoneOrEmpty(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	if r := a.EstimateReduction(orderedmap.New(), StrategyNone, 10); r != 0.0 {
		t.Fatalf("None strategy -> 0.0, got %v", r)
	}
	fs := orderedmap.New()
	fs.Set("a", FieldStats{})
	// Empty field_stats guard: with None -> 0.0 regardless of populated stats.
	if r := a.EstimateReduction(orderedmap.New(), StrategySmartSample, 10); r != 0.0 {
		t.Fatalf("empty field_stats -> 0.0, got %v", r)
	}
}

// min(base + constantRatio*0.2, 0.95). One of two fields is constant -> ratio 0.5;
// SmartSample base 0.5 -> 0.5 + 0.5*0.2 = 0.6.
func TestEstimateReductionConstantBonus(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	fs := orderedmap.New()
	fs.Set("const", FieldStats{Name: "const", IsConstant: true})
	fs.Set("var", FieldStats{Name: "var", IsConstant: false})
	got := a.EstimateReduction(fs, StrategySmartSample, 10)
	if math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("reduction = %v, want 0.6", got)
	}
}

// The cap clamps at 0.95: all-constant fields with a high base saturates.
func TestEstimateReductionCap(t *testing.T) {
	a := NewSmartAnalyzer(DefaultConfig())
	fs := orderedmap.New()
	fs.Set("a", FieldStats{Name: "a", IsConstant: true})
	// ClusterSample base 0.8, ratio 1.0 -> 0.8 + 0.2 = 1.0 -> clamps to 0.95.
	got := a.EstimateReduction(fs, StrategyClusterSample, 10)
	if math.Abs(got-0.95) > 1e-9 {
		t.Fatalf("reduction = %v, want 0.95 (capped)", got)
	}
}

// analyzeAllFields is a test helper that mirrors AnalyzeArray's per-field pass:
// build the sorted-key FieldStats map the crushability tree consumes.
func analyzeAllFields(a *SmartAnalyzer, items []*orderedmap.OrderedMap) *orderedmap.OrderedMap {
	keys := map[string]struct{}{}
	for _, it := range items {
		if it == nil {
			continue
		}
		for _, k := range it.Keys() {
			keys[k] = struct{}{}
		}
	}
	ordered := sortedKeys(keys)
	fs := orderedmap.New()
	for _, k := range ordered {
		fs.Set(k, a.AnalyzeField(k, items))
	}
	return fs
}
