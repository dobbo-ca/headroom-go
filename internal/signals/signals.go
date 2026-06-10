// Package signals provides per-line importance scoring used by the search and
// log compressors to decide which lines to keep. It is a faithful zero-dep port
// of upstream headroom's signals subsystem (keyword_detector + tiered combinator):
// a KeywordDetector that classifies a line into one of five categories using
// context-gated, word-boundary keyword matching, and a Tiered combinator that
// escalates across ordered detectors by confidence.
//
// No CCR, EstimateBloat, or Confidence offload machinery lives here; signals
// emits importance scores only.
package signals

// ImportanceCategory classifies a matched line into one of five categories.
type ImportanceCategory int

const (
	Error ImportanceCategory = iota
	Warning
	Importance
	Security
	Markdown
)

// ImportanceContext is the context of the line being scored. It gates which
// keyword categories are allowed to fire. The zero value is Text (the default).
type ImportanceContext int

const (
	Text ImportanceContext = iota
	Search
	Diff
	Log
)

// Per-category priority constants (keep-importance in [0,1]).
const (
	errorPriority      float32 = 0.95
	securityPriority   float32 = 0.85
	warningPriority    float32 = 0.75
	importancePriority float32 = 0.6
	markdownPriority   float32 = 0.45
)

// keywordConfidence is the fixed confidence emitted on every keyword match.
// It equals escalateThreshold so a keyword match always escalates in a Tiered
// chain.
const keywordConfidence float32 = 0.7

// escalateThreshold is the Tiered combinator's confidence short-circuit bound.
const escalateThreshold float32 = 0.7

// ImportanceSignal is the result of scoring one line. A nil Category means no
// match. Priority is the keep-importance in [0,1] (0=drop first, 1=keep at all
// costs); Confidence is detector certainty in [0,1]. Priority and Confidence are
// deliberately kept separate (there is no combined score).
type ImportanceSignal struct {
	Category   *ImportanceCategory
	Priority   float32
	Confidence float32
}

// Neutral returns the no-match signal {nil, 0, 0}. Used as the starting/best
// value and the no-match return.
func Neutral() ImportanceSignal { return ImportanceSignal{} }

// Matched constructs a matched signal for the given category, priority, and
// confidence.
func Matched(cat ImportanceCategory, prio, conf float32) ImportanceSignal {
	c := cat
	return ImportanceSignal{Category: &c, Priority: prio, Confidence: conf}
}

// IsMatch reports whether a category matched.
func (s ImportanceSignal) IsMatch() bool { return s.Category != nil }

// LineImportanceDetector scores a single line in a given context.
type LineImportanceDetector interface {
	Score(line string, ctx ImportanceContext) ImportanceSignal
}

// priorityFor maps a category to its per-category priority constant.
func priorityFor(cat ImportanceCategory) float32 {
	switch cat {
	case Error:
		return errorPriority
	case Security:
		return securityPriority
	case Warning:
		return warningPriority
	case Importance:
		return importancePriority
	case Markdown:
		return markdownPriority
	default:
		return 0
	}
}
