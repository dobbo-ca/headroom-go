package smartcrusher

import (
	"sort"

	"github.com/iancoleman/orderedmap"
)

// AnchorSelector picks the "anchor" item indices a crush must always keep
// [ref: builder.rs (anchor_selector) + (d) SAMPLING]. The MVP selection is
// pattern-agnostic: the first min(3,n) indices, the last 2 indices, and every
// index whose item matches a query anchor (ItemMatchesAnchors). Pattern-specific
// anchoring (time-series windows, search-result heads, log clusters) is DEFERRED;
// map_to_anchor_pattern hands SmartSample -> Generic, so Generic is the only
// pattern the MVP exercises.

// AnchorPattern names the array shape the selector is anchoring for. Only Generic
// is exercised in the MVP; the other variants exist so planning.go's
// mapToAnchorPattern (Task 13) has a stable target set.
type AnchorPattern int

const (
	// AnchorGeneric is the default/fallback pattern (SmartSample -> Generic).
	AnchorGeneric AnchorPattern = iota
	// AnchorTimeSeries anchors time-ordered arrays.
	//
	// DEFERRED (Plan 4+): no distinct MVP anchoring; treated as Generic.
	AnchorTimeSeries
	// AnchorSearchResults anchors ranked search-result arrays (from TopN).
	//
	// DEFERRED (Plan 4+): no distinct MVP anchoring; treated as Generic.
	AnchorSearchResults
	// AnchorLogs anchors clustered log arrays (from ClusterSample).
	//
	// DEFERRED (Plan 4+): no distinct MVP anchoring; treated as Generic.
	AnchorLogs
)

// AnchorConfig holds the head/tail anchor counts [ref: (d) SAMPLING first_anchors
// = 3, last_anchors = 2].
type AnchorConfig struct {
	FirstAnchors int
	LastAnchors  int
}

// DefaultAnchorConfig returns the MVP anchor counts: 3 leading, 2 trailing.
func DefaultAnchorConfig() AnchorConfig {
	return AnchorConfig{FirstAnchors: 3, LastAnchors: 2}
}

// AnchorSelector selects anchor indices per its AnchorConfig.
type AnchorSelector struct {
	config AnchorConfig
}

// NewAnchorSelector builds a selector from cfg.
func NewAnchorSelector(cfg AnchorConfig) *AnchorSelector {
	return &AnchorSelector{config: cfg}
}

// SelectAnchors returns the ascending, deduplicated, in-bounds set of anchor
// indices for items: the first min(FirstAnchors, n), the last LastAnchors, and
// every index whose item matches a query anchor. maxItems and pattern are accepted
// for signature parity with the DEFERRED per-pattern selectors but do not affect
// the MVP Generic selection. An empty array yields an empty set.
func (s *AnchorSelector) SelectAnchors(items []*orderedmap.OrderedMap, maxItems int, pattern AnchorPattern, query string) []int {
	_ = maxItems
	_ = pattern

	n := len(items)
	if n == 0 {
		return []int{}
	}

	keep := make(map[int]struct{})

	// First min(FirstAnchors, n) indices.
	first := s.config.FirstAnchors
	if first > n {
		first = n
	}
	for i := 0; i < first; i++ {
		keep[i] = struct{}{}
	}

	// Last LastAnchors indices (clamped to 0 when n < LastAnchors).
	lastStart := n - s.config.LastAnchors
	if lastStart < 0 {
		lastStart = 0
	}
	for i := lastStart; i < n; i++ {
		keep[i] = struct{}{}
	}

	// Query-anchor matches (skipped entirely when the query is empty, since an
	// empty anchor set never matches).
	if query != "" {
		anchors := ExtractQueryAnchors(query)
		if len(anchors) > 0 {
			for i, item := range items {
				if item == nil {
					continue
				}
				if ItemMatchesAnchors(item, anchors) {
					keep[i] = struct{}{}
				}
			}
		}
	}

	result := make([]int, 0, len(keep))
	for i := range keep {
		result = append(result, i)
	}
	sort.Ints(result)
	return result
}
