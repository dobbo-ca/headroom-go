package smartcrusher

import (
	"math"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// SmartCrusherPlanner turns an ArrayAnalysis into a CompressionPlan whose
// keep_indices is a sorted list of original indices to retain [ref: planning.rs].
// It emits NO CCR markers and does no byte rewriting (downstream concerns). The MVP
// covers the create_plan dispatcher (Skip short-circuit + SmartSample routing),
// plan_smart_sample, and the shared signal helpers; the heavy per-pattern planners
// (TopN/ClusterSample/TimeSeries) are DEFERRED and route to planSmartSample.
type SmartCrusherPlanner struct {
	Config         Config
	AnchorSelector *AnchorSelector
	Scorer         Scorer
	Analyzer       *SmartAnalyzer
	Constraints    []Constraint
}

// NewSmartCrusherPlanner assembles a planner from its collaborators.
func NewSmartCrusherPlanner(config Config, anchorSelector *AnchorSelector, scorer Scorer, analyzer *SmartAnalyzer, constraints []Constraint) *SmartCrusherPlanner {
	return &SmartCrusherPlanner{
		Config:         config,
		AnchorSelector: anchorSelector,
		Scorer:         scorer,
		Analyzer:       analyzer,
		Constraints:    constraints,
	}
}

// CreatePlan dispatches on analysis.RecommendedStrategy [ref: planning.rs
// create_plan]. effectiveMaxItems overrides config.MaxItemsAfterCrush when non-nil.
// A Skip verdict short-circuits to keep-all (0..n) before any dispatch. All
// non-Skip strategies route through planSmartSample in the MVP; the heavy planners
// are DEFERRED. preserveFields and itemStrings are TOIN/perf hints (callers pass
// nil).
func (p *SmartCrusherPlanner) CreatePlan(analysis *ArrayAnalysis, items []*orderedmap.OrderedMap, queryContext string, preserveFields []string, effectiveMaxItems *int, itemStrings []string) *CompressionPlan {
	maxItems := p.Config.MaxItemsAfterCrush
	if effectiveMaxItems != nil {
		maxItems = *effectiveMaxItems
	}

	plan := DefaultCompressionPlan()
	plan.Strategy = analysis.RecommendedStrategy
	if p.Config.FactorOutConstants && analysis.ConstantFields != nil {
		plan.ConstantFields = cloneOrderedMap(analysis.ConstantFields)
	} else {
		plan.ConstantFields = orderedmap.New()
	}

	// SKIP short-circuit: keep ALL 0..n and return (defensive; crushArray
	// short-circuits earlier).
	if analysis.RecommendedStrategy == StrategySkip {
		n := len(items)
		keep := make([]int, 0, n)
		for i := 0; i < n; i++ {
			keep = append(keep, i)
		}
		plan.KeepIndices = keep
		return &plan
	}

	// Dispatch. The heavy planners (TimeSeries/ClusterSample/TopN) are DEFERRED
	// (Plan 4+) and route to planSmartSample; SmartSample/None already route there.
	return p.planSmartSample(analysis, items, &plan, queryContext, preserveFields, maxItems, itemStrings)
}

// planSmartSample is the MVP default/fallback planner [ref: planning.rs
// plan_smart_sample]. It unions anchor, constraint, anomaly, change-point, and
// query-signal indices, then resolves the final keep set through PrioritizeIndices.
func (p *SmartCrusherPlanner) planSmartSample(analysis *ArrayAnalysis, items []*orderedmap.OrderedMap, plan *CompressionPlan, queryContext string, preserveFields []string, maxItems int, itemStrings []string) *CompressionPlan {
	n := len(items)
	keep := make(map[int]struct{})
	anyItems := toAnySlice(items)

	// Step1: anchors (pattern -> Generic in the MVP).
	pattern := mapToAnchorPattern(StrategySmartSample)
	for _, idx := range p.AnchorSelector.SelectAnchors(items, maxItems, pattern, queryOrNone(queryContext)) {
		keep[idx] = struct{}{}
	}

	// Step2: constraints (union over the constraint stack).
	p.applyConstraints(items, itemStrings, keep)

	// Step3: numeric anomalies (per field_stats).
	if analysis.FieldStats != nil {
		for _, name := range analysis.FieldStats.Keys() {
			v, _ := analysis.FieldStats.Get(name)
			stats, ok := v.(FieldStats)
			if !ok {
				continue
			}
			forEachAnomaly(name, stats, anyItems, p.Config.VarianceThreshold, keep)
		}
	}

	// Step4: change points (gated on PreserveChangePoints; window -1..=1 clamped).
	if p.Config.PreserveChangePoints && analysis.FieldStats != nil {
		for _, name := range analysis.FieldStats.Keys() {
			v, _ := analysis.FieldStats.Get(name)
			stats, ok := v.(FieldStats)
			if !ok {
				continue
			}
			for _, cp := range stats.ChangePoints {
				for offset := -1; offset <= 1; offset++ {
					idx := cp + offset
					if idx >= 0 && idx < n {
						keep[idx] = struct{}{}
					}
				}
			}
		}
	}

	// Step5/6: query signals (deterministic anchor match + probabilistic scoring).
	p.applyQuerySignals(items, queryContext, itemStrings, keep, false)

	// Step7: preserve-field matches (TOIN; callers pass nil -> no-op guard).
	p.applyPreserveFieldMatches(items, queryContext, preserveFields, keep)

	// Step8: prioritize under budget and sort ascending.
	finalKeep := PrioritizeIndices(&p.Config, keep, anyItems, n, analysis, maxItems)
	plan.KeepIndices = sortedIntSet(finalKeep)
	return plan
}

// applyConstraints unions every constraint's must-keep indices into keep [ref:
// planning.rs apply_constraints].
func (p *SmartCrusherPlanner) applyConstraints(items []*orderedmap.OrderedMap, itemStrings []string, keep map[int]struct{}) {
	for _, c := range p.Constraints {
		for _, idx := range c.MustKeep(items, itemStrings) {
			if idx >= 0 && idx < len(items) {
				keep[idx] = struct{}{}
			}
		}
	}
}

// applyQuerySignals inserts query-relevant indices via two paths [ref: planning.rs
// apply_query_signals]: (a) deterministic anchor-substring matching, and (b)
// probabilistic relevance scoring against the RelevanceThreshold. An empty query is
// a no-op. keepExistingOnly, when true, skips indices already present (unused in the
// MVP planSmartSample call, which passes false).
func (p *SmartCrusherPlanner) applyQuerySignals(items []*orderedmap.OrderedMap, queryContext string, itemStrings []string, keep map[int]struct{}, keepExistingOnly bool) {
	if queryContext == "" {
		return
	}

	// (a) Deterministic anchor matching.
	anchors := ExtractQueryAnchors(queryContext)
	if len(anchors) > 0 {
		for i, item := range items {
			if keepExistingOnly {
				if _, ok := keep[i]; ok {
					continue
				}
			}
			if item != nil && ItemMatchesAnchors(item, anchors) {
				keep[i] = struct{}{}
			}
		}
	}

	// (b) Probabilistic scoring.
	strs := itemStrings
	if strs == nil {
		strs = make([]string, len(items))
		for i, item := range items {
			if item == nil {
				strs[i] = ""
				continue
			}
			strs[i] = pythonSafeJSONDumps(item)
		}
	}
	scores := p.Scorer.ScoreBatch(strs, queryContext)
	for i, sc := range scores {
		if i >= len(items) {
			break
		}
		if keepExistingOnly {
			if _, ok := keep[i]; ok {
				continue
			}
		}
		if sc.Score >= p.Config.RelevanceThreshold {
			keep[i] = struct{}{}
		}
	}
}

// applyPreserveFieldMatches is the TOIN preserve-field seam [ref: planning.rs
// apply_preserve_field_matches]. It is a no-op when preserveFields is empty or the
// query is empty; the MVP always passes nil preserveFields, so the loop never runs.
func (p *SmartCrusherPlanner) applyPreserveFieldMatches(items []*orderedmap.OrderedMap, queryContext string, preserveFields []string, keep map[int]struct{}) {
	if len(preserveFields) == 0 || queryContext == "" {
		return
	}
	hashes := make([]string, 0, len(preserveFields))
	for _, f := range preserveFields {
		hashes = append(hashes, HashFieldName(f))
	}
	for i, item := range items {
		if itemHasPreserveFieldMatch(item, hashes, queryContext) {
			keep[i] = struct{}{}
		}
	}
}

// mapToAnchorPattern maps a strategy to the anchor pattern used for selection [ref:
// planning.rs map_to_anchor_pattern]. In the MVP only Generic (SmartSample) is
// exercised; the others exist for parity with the DEFERRED planners.
func mapToAnchorPattern(strategy CompressionStrategy) AnchorPattern {
	switch strategy {
	case StrategyTimeSeries:
		return AnchorTimeSeries
	case StrategyTopN:
		return AnchorSearchResults
	case StrategyClusterSample:
		return AnchorLogs
	default:
		return AnchorGeneric
	}
}

// queryOrNone returns the query when non-empty, else the empty string (the Go
// stand-in for None) [ref: planning.rs query_or_none].
func queryOrNone(q string) string {
	return q
}

// forEachAnomaly inserts indices whose numeric field value deviates from the mean
// by more than variance_threshold*std [ref: planning.rs for_each_anomaly]. It is a
// no-op for non-numeric fields, missing mean/variance, variance<=0, or std<=0; NaN
// and non-numeric/missing values are skipped per item.
func forEachAnomaly(fieldName string, stats FieldStats, items []any, varianceThreshold float64, keep map[int]struct{}) {
	if stats.FieldType != "numeric" || stats.MeanVal == nil || stats.Variance == nil {
		return
	}
	if *stats.Variance <= 0 {
		return
	}
	std := math.Sqrt(*stats.Variance)
	if std <= 0 {
		return
	}
	threshold := varianceThreshold * std
	mean := *stats.MeanVal
	for i, item := range items {
		obj, ok := item.(*orderedmap.OrderedMap)
		if !ok || obj == nil {
			continue
		}
		raw, present := obj.Get(fieldName)
		if !present {
			continue
		}
		num, okNum := asFloat64(raw)
		if !okNum {
			continue
		}
		if math.Abs(num-mean) > threshold {
			keep[i] = struct{}{}
		}
	}
}

// itemHasPreserveFieldMatch is the TOIN preserve-field predicate [ref: planning.rs
// item_has_preserve_field_match]. It is present but unexercised in the MVP (callers
// pass nil preserveFields, so applyPreserveFieldMatches returns before reaching it).
//
// DEFERRED (Plan 4+): TOIN / field_semantics is not wired; this predicate exists so
// the planning surface matches upstream.
func itemHasPreserveFieldMatch(item *orderedmap.OrderedMap, preserveFieldHashes []string, queryContext string) bool {
	if queryContext == "" || item == nil {
		return false
	}
	queryLower := strings.ToLower(queryContext)
	for _, k := range item.Keys() {
		h := HashFieldName(k)
		matched := false
		for _, ph := range preserveFieldHashes {
			if ph == h {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		v, _ := item.Get(k)
		if v == nil {
			continue
		}
		valueStr := strings.ToLower(valueToString(v))
		if valueStr != "" &&
			(strings.Contains(valueStr, queryLower) || strings.Contains(queryLower, valueStr)) {
			return true
		}
	}
	return false
}

// valueToString renders a preserve-field value: strings pass through raw, other
// scalars/containers use the compact serializer.
func valueToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return compactSerialize(v)
}

// cloneOrderedMap makes a shallow copy of an ordered map preserving key order.
func cloneOrderedMap(om *orderedmap.OrderedMap) *orderedmap.OrderedMap {
	out := orderedmap.New()
	for _, k := range om.Keys() {
		v, _ := om.Get(k)
		out.Set(k, v)
	}
	return out
}
