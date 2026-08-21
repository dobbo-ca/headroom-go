package smartcrusher

// Config holds the SmartCrusher tuning knobs. It is a schema-preserving
// compressor: output contains only items from the original array. Fields tagged
// DEFERRED-consumer below are defined for parity but not yet exercised in the MVP.
type Config struct {
	Enabled                 bool
	MinItemsToAnalyze       int
	MinTokensToCrush        int
	VarianceThreshold       float64
	UniquenessThreshold     float64
	SimilarityThreshold     float64
	MaxItemsAfterCrush      int
	PreserveChangePoints    bool
	FactorOutConstants      bool
	IncludeSummaries        bool
	UseFeedbackHints        bool
	ToinConfidenceThreshold float64
	DedupIdenticalItems     bool
	FirstFraction           float64
	LastFraction            float64
	RelevanceThreshold      float64
	// LosslessMinSavingsRatio is the savings gate used in crushArray:
	// savings = 1 - len(rendered)/len(input) (BYTES); savings < this ratio falls
	// through to the lossy path. Upstream diverges: config.rs lowered the default
	// to 0.15, but crusher.rs docs, the parity fixtures, and this plan's brief use
	// 0.30. The MVP-effective default is 0.30; tests override to 0.99 to force the
	// lossy path.
	LosslessMinSavingsRatio float64
	EnableCCRMarker         bool

	CompactionCoreFieldFraction      float64
	CompactionHeterogeneousCoreRatio float64
	CompactionMaxFlattenInnerKeys    int
	CompactionMinBuckets             int
	CompactionMaxBuckets             int
}

// DefaultConfig returns the canonical SmartCrusher defaults [ref: (d) constants].
func DefaultConfig() Config {
	return Config{
		Enabled:                 true,
		MinItemsToAnalyze:       5,
		MinTokensToCrush:        200,
		VarianceThreshold:       2.0,
		UniquenessThreshold:     0.1,
		SimilarityThreshold:     0.8,
		MaxItemsAfterCrush:      15,
		PreserveChangePoints:    true,
		FactorOutConstants:      false,
		IncludeSummaries:        false,
		UseFeedbackHints:        true,
		ToinConfidenceThreshold: 0.5,
		DedupIdenticalItems:     true,
		FirstFraction:           0.3,
		LastFraction:            0.15,
		RelevanceThreshold:      0.3,
		LosslessMinSavingsRatio: 0.30, // MVP effective; config.rs default is 0.15.
		EnableCCRMarker:         true,

		CompactionCoreFieldFraction:      0.8,
		CompactionHeterogeneousCoreRatio: 0.6,
		CompactionMaxFlattenInnerKeys:    6,
		CompactionMinBuckets:             2,
		CompactionMaxBuckets:             8,
	}
}
