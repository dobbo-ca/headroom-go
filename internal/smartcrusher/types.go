package smartcrusher

import "github.com/iancoleman/orderedmap"

// CompressionStrategy enumerates the recommended crush strategies. In the MVP
// only None/Skip/SmartSample are actively assigned; TimeSeries/ClusterSample/TopN
// are defined-but-unassigned parity variants.
type CompressionStrategy int

const (
	StrategyNone CompressionStrategy = iota
	StrategySkip
	StrategyTimeSeries
	StrategyClusterSample
	StrategyTopN
	StrategySmartSample
)

// String returns the parity-pinned lowercase identifier (byte-pinned; Decision B
// does NOT relax these). Note StrategyClusterSample is "cluster", NOT
// "cluster_sample".
func (s CompressionStrategy) String() string {
	switch s {
	case StrategyNone:
		return "none"
	case StrategySkip:
		return "skip"
	case StrategyTimeSeries:
		return "time_series"
	case StrategyClusterSample:
		return "cluster"
	case StrategyTopN:
		return "top_n"
	case StrategySmartSample:
		return "smart_sample"
	default:
		return "none"
	}
}

// TopValue is one entry of a field's frequency-descending top-values list.
type TopValue struct {
	Value string
	Count int
}

// SummaryRange is a summarized index span. Summary is *any-typed to distinguish
// absent from a zero value; SummaryRanges is empty in the MVP.
type SummaryRange struct {
	Start   int
	End     int
	Summary any
}

// FieldStats captures per-field statistics. Option<T> upstream fields map to Go
// pointers so absent (nil) and zero (&0.0) stay distinct. FieldType domain:
// numeric|string|boolean|object|array|null.
type FieldStats struct {
	Name          string
	FieldType     string
	Count         int
	UniqueCount   int
	UniqueRatio   float64
	IsConstant    bool
	ConstantValue *any
	MinVal        *float64
	MaxVal        *float64
	MeanVal       *float64
	Variance      *float64
	ChangePoints  []int
	AvgLength     *float64
	TopValues     []TopValue
}

// CrushabilityAnalysis is the analyzer's verdict for one array.
type CrushabilityAnalysis struct {
	Crushable           bool
	Confidence          float64
	Reason              string
	SignalsPresent      []string
	SignalsAbsent       []string
	HasIDField          bool
	IDUniqueness        float64
	AvgStringUniqueness float64
	HasScoreField       bool
	ErrorItemCount      int
	AnomalyCount        int
}

// SkipAnalysis builds a not-crushable CrushabilityAnalysis with metrics zeroed
// and slices EMPTY but non-nil.
func SkipAnalysis(reason string, confidence float64) CrushabilityAnalysis {
	return CrushabilityAnalysis{
		Crushable:      false,
		Confidence:     confidence,
		Reason:         reason,
		SignalsPresent: []string{},
		SignalsAbsent:  []string{},
	}
}

// ArrayAnalysis is the full analysis of one array. FieldStats/ConstantFields are
// ordered maps (key order load-bearing). DetectedPattern domain:
// time_series|logs|search_results|generic.
type ArrayAnalysis struct {
	ItemCount           int
	FieldStats          *orderedmap.OrderedMap
	DetectedPattern     string
	RecommendedStrategy CompressionStrategy
	ConstantFields      *orderedmap.OrderedMap
	EstimatedReduction  float64
	Crushability        *CrushabilityAnalysis
}

// CompressionPlan describes how to compress one array. ClusterField/SortField are
// nil in the MVP; SummaryRanges is empty.
type CompressionPlan struct {
	Strategy       CompressionStrategy
	KeepIndices    []int
	ConstantFields *orderedmap.OrderedMap
	SummaryRanges  []SummaryRange
	ClusterField   *string
	SortField      *string
	KeepCount      int
}

// DefaultCompressionPlan returns a None-strategy plan with empty indices/ranges,
// nil cluster/sort fields, and the parity-pinned KeepCount of 10.
func DefaultCompressionPlan() CompressionPlan {
	return CompressionPlan{
		Strategy:       StrategyNone,
		KeepIndices:    []int{},
		ConstantFields: orderedmap.New(),
		SummaryRanges:  []SummaryRange{},
		ClusterField:   nil,
		SortField:      nil,
		KeepCount:      10,
	}
}

// CrushResult is the INTERNAL 4-field result the crusher computes. It is RICHER
// than the offloads.CrushResult seam type (2 fields) and stays internal — do NOT
// widen the seam. Conflating the two is the #1 porting error.
type CrushResult struct {
	Compressed  string
	Original    string
	WasModified bool
	Strategy    string
}

// PassthroughResult builds a no-op CrushResult (Compressed==Original==content,
// WasModified false, Strategy "passthrough").
func PassthroughResult(content string) CrushResult {
	return CrushResult{
		Compressed:  content,
		Original:    content,
		WasModified: false,
		Strategy:    "passthrough",
	}
}
