package smartcrusher

import "testing"

// TestCompressionStrategyString pins the parity-pinned enum strings. Note
// StrategyClusterSample renders "cluster" (NOT "cluster_sample").
func TestCompressionStrategyString(t *testing.T) {
	cases := []struct {
		strat CompressionStrategy
		want  string
	}{
		{StrategyNone, "none"},
		{StrategySkip, "skip"},
		{StrategyTimeSeries, "time_series"},
		{StrategyClusterSample, "cluster"},
		{StrategyTopN, "top_n"},
		{StrategySmartSample, "smart_sample"},
	}
	for _, tc := range cases {
		if got := tc.strat.String(); got != tc.want {
			t.Errorf("CompressionStrategy(%d).String() = %q, want %q", tc.strat, got, tc.want)
		}
	}
}

// TestDefaultCompressionPlan pins the parity default KeepCount and the empty/nil
// shape.
func TestDefaultCompressionPlan(t *testing.T) {
	p := DefaultCompressionPlan()
	if p.Strategy != StrategyNone {
		t.Errorf("Strategy: want StrategyNone, got %v", p.Strategy)
	}
	if p.KeepCount != 10 {
		t.Errorf("KeepCount: want 10, got %d", p.KeepCount)
	}
	if p.KeepIndices == nil || len(p.KeepIndices) != 0 {
		t.Errorf("KeepIndices: want empty non-nil slice, got %#v", p.KeepIndices)
	}
	if p.SummaryRanges == nil || len(p.SummaryRanges) != 0 {
		t.Errorf("SummaryRanges: want empty non-nil slice, got %#v", p.SummaryRanges)
	}
	if p.ClusterField != nil {
		t.Errorf("ClusterField: want nil, got %v", *p.ClusterField)
	}
	if p.SortField != nil {
		t.Errorf("SortField: want nil, got %v", *p.SortField)
	}
	if p.ConstantFields == nil {
		t.Errorf("ConstantFields: want non-nil orderedmap, got nil")
	}
}

// TestPassthroughResult pins the internal 4-field CrushResult shape for the
// passthrough case.
func TestPassthroughResult(t *testing.T) {
	r := PassthroughResult("x")
	if r.Compressed != "x" {
		t.Errorf("Compressed: want \"x\", got %q", r.Compressed)
	}
	if r.Original != "x" {
		t.Errorf("Original: want \"x\", got %q", r.Original)
	}
	if r.WasModified {
		t.Errorf("WasModified: want false, got %v", r.WasModified)
	}
	if r.Strategy != "passthrough" {
		t.Errorf("Strategy: want \"passthrough\", got %q", r.Strategy)
	}
}

// TestSkipAnalysis pins the SkipAnalysis constructor: Crushable=false and slices
// EMPTY non-nil.
func TestSkipAnalysis(t *testing.T) {
	a := SkipAnalysis("r", 0.6)
	if a.Crushable {
		t.Errorf("Crushable: want false, got %v", a.Crushable)
	}
	if a.Reason != "r" {
		t.Errorf("Reason: want \"r\", got %q", a.Reason)
	}
	if a.Confidence != 0.6 {
		t.Errorf("Confidence: want 0.6, got %v", a.Confidence)
	}
	if a.SignalsPresent == nil || len(a.SignalsPresent) != 0 {
		t.Errorf("SignalsPresent: want empty non-nil slice, got %#v", a.SignalsPresent)
	}
	if a.SignalsAbsent == nil || len(a.SignalsAbsent) != 0 {
		t.Errorf("SignalsAbsent: want empty non-nil slice, got %#v", a.SignalsAbsent)
	}
}
