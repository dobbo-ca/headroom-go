package smartcrusher

import (
	"sort"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/relevance"
	"github.com/iancoleman/orderedmap"
)

func newTestPlanner(cfg Config) *SmartCrusherPlanner {
	return NewSmartCrusherPlanner(
		cfg,
		NewAnchorSelector(DefaultAnchorConfig()),
		relevance.NewHybridScorer(),
		NewSmartAnalyzer(cfg),
		defaultOSSConstraints(),
	)
}

func TestCreatePlan_SkipKeepsAll(t *testing.T) {
	cfg := DefaultConfig()
	p := newTestPlanner(cfg)
	items := decodeArrayObjs(t, `[{"a":1},{"a":2},{"a":3}]`)
	analysis := &ArrayAnalysis{
		ItemCount:           len(items),
		FieldStats:          orderedmap.New(),
		RecommendedStrategy: StrategySkip,
		ConstantFields:      orderedmap.New(),
	}
	plan := p.CreatePlan(analysis, items, "", nil, nil, nil)
	if plan.Strategy != StrategySkip {
		t.Fatalf("plan.Strategy = %v, want Skip", plan.Strategy)
	}
	want := []int{0, 1, 2}
	if !sortedEqual(plan.KeepIndices, want) {
		t.Fatalf("Skip keep_indices = %v, want %v (all 0..n)", plan.KeepIndices, want)
	}
}

func TestCreatePlan_SmartSampleSortedAscending(t *testing.T) {
	cfg := DefaultConfig()
	p := newTestPlanner(cfg)
	items := make([]*orderedmap.OrderedMap, 0, 6)
	for i := 0; i < 6; i++ {
		om := orderedmap.New()
		om.Set("i", float64(i))
		items = append(items, om)
	}
	analysis := &ArrayAnalysis{
		ItemCount:           len(items),
		FieldStats:          orderedmap.New(),
		RecommendedStrategy: StrategySmartSample,
		ConstantFields:      orderedmap.New(),
	}
	plan := p.CreatePlan(analysis, items, "", nil, nil, nil)
	if !sort.IntsAreSorted(plan.KeepIndices) {
		t.Fatalf("keep_indices not ascending: %v", plan.KeepIndices)
	}
	// All indices in bounds.
	for _, idx := range plan.KeepIndices {
		if idx < 0 || idx >= len(items) {
			t.Fatalf("keep index %d out of bounds (n=%d)", idx, len(items))
		}
	}
}

func TestPlanSmartSample_EmptyQueryNoOpSignals(t *testing.T) {
	cfg := DefaultConfig()
	// Force over-budget so anchors/constraints actually run.
	p := newTestPlanner(cfg)
	items := make([]*orderedmap.OrderedMap, 0, 30)
	for i := 0; i < 30; i++ {
		om := orderedmap.New()
		om.Set("i", float64(i))
		items = append(items, om)
	}
	analysis := &ArrayAnalysis{
		ItemCount:           len(items),
		FieldStats:          orderedmap.New(),
		RecommendedStrategy: StrategySmartSample,
		ConstantFields:      orderedmap.New(),
	}
	max := 10
	plan := p.CreatePlan(analysis, items, "", nil, &max, nil)
	// first-3 + last-2 anchors present when room.
	for _, want := range []int{0, 1, 2, 28, 29} {
		found := false
		for _, idx := range plan.KeepIndices {
			if idx == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("empty-query plan missing anchor %d: %v", want, plan.KeepIndices)
		}
	}
}

func TestForEachAnomaly_Guards(t *testing.T) {
	cfg := DefaultConfig()
	// Nine values clustered at 10 and one outlier at 200 (index 9). mean=29,
	// variance=3610 (sample, n-1), std=60.083, threshold=2*std=120.17; the outlier
	// deviates by 171 > 120.17 so it fires, while the cluster (dev 19) does not.
	items := decodeArrayObjs(t, `[{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":10},{"v":200}]`)
	keep := map[int]struct{}{}

	// Non-numeric field: no-op.
	forEachAnomaly("v", FieldStats{Name: "v", FieldType: "string"}, toAnySlice(items), cfg.VarianceThreshold, keep)
	if len(keep) != 0 {
		t.Fatalf("non-numeric field flagged anomalies: %v", sortedIntSet(keep))
	}

	// Missing mean/variance: no-op.
	forEachAnomaly("v", FieldStats{Name: "v", FieldType: "numeric"}, toAnySlice(items), cfg.VarianceThreshold, keep)
	if len(keep) != 0 {
		t.Fatalf("missing mean/variance flagged anomalies: %v", sortedIntSet(keep))
	}

	// variance <= 0: no-op.
	mean := 29.0
	zero := 0.0
	forEachAnomaly("v", FieldStats{Name: "v", FieldType: "numeric", MeanVal: &mean, Variance: &zero}, toAnySlice(items), cfg.VarianceThreshold, keep)
	if len(keep) != 0 {
		t.Fatalf("variance<=0 flagged anomalies: %v", sortedIntSet(keep))
	}

	// Real anomaly fires; the clustered rows do not.
	variance := 3610.0
	forEachAnomaly("v", FieldStats{Name: "v", FieldType: "numeric", MeanVal: &mean, Variance: &variance}, toAnySlice(items), cfg.VarianceThreshold, keep)
	if _, ok := keep[9]; !ok {
		t.Fatalf("forEachAnomaly missed outlier index 9: %v", sortedIntSet(keep))
	}
	if len(keep) != 1 {
		t.Fatalf("forEachAnomaly flagged clustered rows too: %v", sortedIntSet(keep))
	}
}

func TestMapToAnchorPattern(t *testing.T) {
	cases := []struct {
		strategy CompressionStrategy
		want     AnchorPattern
	}{
		{StrategyTimeSeries, AnchorTimeSeries},
		{StrategyTopN, AnchorSearchResults},
		{StrategyClusterSample, AnchorLogs},
		{StrategySmartSample, AnchorGeneric},
		{StrategyNone, AnchorGeneric},
	}
	for _, c := range cases {
		if got := mapToAnchorPattern(c.strategy); got != c.want {
			t.Fatalf("mapToAnchorPattern(%v) = %v, want %v", c.strategy, got, c.want)
		}
	}
}

func TestQueryOrNone(t *testing.T) {
	if queryOrNone("") != "" {
		t.Fatalf("queryOrNone(empty) should be empty")
	}
	if queryOrNone("x") != "x" {
		t.Fatalf("queryOrNone(x) should be x")
	}
}

func TestPlanSmartSample_ChangePointsGated(t *testing.T) {
	// With PreserveChangePoints=false the change-point window must NOT add rows.
	cfg := DefaultConfig()
	cfg.PreserveChangePoints = false
	p := newTestPlanner(cfg)
	items := make([]*orderedmap.OrderedMap, 0, 30)
	for i := 0; i < 30; i++ {
		om := orderedmap.New()
		om.Set("i", float64(i))
		items = append(items, om)
	}
	fs := orderedmap.New()
	fs.Set("i", FieldStats{Name: "i", FieldType: "numeric", ChangePoints: []int{15}})
	analysis := &ArrayAnalysis{
		ItemCount:           len(items),
		FieldStats:          fs,
		RecommendedStrategy: StrategySmartSample,
		ConstantFields:      orderedmap.New(),
	}
	max := 5
	plan := p.CreatePlan(analysis, items, "", nil, &max, nil)
	// index 15 (change point) should not be forced in when gate is off; but it may
	// coincidentally appear via other signals. Assert it is NOT present for these
	// benign, non-anomalous items (no anchor/error/anomaly hits index 15).
	for _, idx := range plan.KeepIndices {
		if idx == 14 || idx == 16 {
			t.Fatalf("change-point neighbor %d kept despite gate off: %v", idx, plan.KeepIndices)
		}
	}
}

func sortedEqual(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]int(nil), got...)
	w := append([]int(nil), want...)
	sort.Ints(g)
	sort.Ints(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}
