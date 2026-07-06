package smartcrusher

import "testing"

// TestDefaultConfig pins every canonical default [ref: (d) constants]. The
// lossless savings gate is the MVP-effective 0.30 (config.rs says 0.15; see the
// divergence comment in config.go).
func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()

	if !c.Enabled {
		t.Errorf("Enabled: want true, got %v", c.Enabled)
	}
	if c.MinItemsToAnalyze != 5 {
		t.Errorf("MinItemsToAnalyze: want 5, got %d", c.MinItemsToAnalyze)
	}
	if c.MinTokensToCrush != 200 {
		t.Errorf("MinTokensToCrush: want 200, got %d", c.MinTokensToCrush)
	}
	if c.VarianceThreshold != 2.0 {
		t.Errorf("VarianceThreshold: want 2.0, got %v", c.VarianceThreshold)
	}
	if c.UniquenessThreshold != 0.1 {
		t.Errorf("UniquenessThreshold: want 0.1, got %v", c.UniquenessThreshold)
	}
	if c.SimilarityThreshold != 0.8 {
		t.Errorf("SimilarityThreshold: want 0.8, got %v", c.SimilarityThreshold)
	}
	if c.MaxItemsAfterCrush != 15 {
		t.Errorf("MaxItemsAfterCrush: want 15, got %d", c.MaxItemsAfterCrush)
	}
	if !c.PreserveChangePoints {
		t.Errorf("PreserveChangePoints: want true, got %v", c.PreserveChangePoints)
	}
	if c.FactorOutConstants {
		t.Errorf("FactorOutConstants: want false, got %v", c.FactorOutConstants)
	}
	if c.IncludeSummaries {
		t.Errorf("IncludeSummaries: want false, got %v", c.IncludeSummaries)
	}
	if !c.UseFeedbackHints {
		t.Errorf("UseFeedbackHints: want true, got %v", c.UseFeedbackHints)
	}
	if c.ToinConfidenceThreshold != 0.5 {
		t.Errorf("ToinConfidenceThreshold: want 0.5, got %v", c.ToinConfidenceThreshold)
	}
	if !c.DedupIdenticalItems {
		t.Errorf("DedupIdenticalItems: want true, got %v", c.DedupIdenticalItems)
	}
	if c.FirstFraction != 0.3 {
		t.Errorf("FirstFraction: want 0.3, got %v", c.FirstFraction)
	}
	if c.LastFraction != 0.15 {
		t.Errorf("LastFraction: want 0.15, got %v", c.LastFraction)
	}
	if c.RelevanceThreshold != 0.3 {
		t.Errorf("RelevanceThreshold: want 0.3, got %v", c.RelevanceThreshold)
	}
	if c.LosslessMinSavingsRatio != 0.30 {
		t.Errorf("LosslessMinSavingsRatio: want 0.30 (MVP effective), got %v", c.LosslessMinSavingsRatio)
	}
	if !c.EnableCCRMarker {
		t.Errorf("EnableCCRMarker: want true, got %v", c.EnableCCRMarker)
	}
	if c.CompactionCoreFieldFraction != 0.8 {
		t.Errorf("CompactionCoreFieldFraction: want 0.8, got %v", c.CompactionCoreFieldFraction)
	}
	if c.CompactionHeterogeneousCoreRatio != 0.6 {
		t.Errorf("CompactionHeterogeneousCoreRatio: want 0.6, got %v", c.CompactionHeterogeneousCoreRatio)
	}
	if c.CompactionMaxFlattenInnerKeys != 6 {
		t.Errorf("CompactionMaxFlattenInnerKeys: want 6, got %d", c.CompactionMaxFlattenInnerKeys)
	}
	if c.CompactionMinBuckets != 2 {
		t.Errorf("CompactionMinBuckets: want 2, got %d", c.CompactionMinBuckets)
	}
	if c.CompactionMaxBuckets != 8 {
		t.Errorf("CompactionMaxBuckets: want 8, got %d", c.CompactionMaxBuckets)
	}
}
