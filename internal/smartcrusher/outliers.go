package smartcrusher

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// Structural-outlier, rare-status, and error-item preservation detectors
// [ref: outliers.rs]. All three are pure MVP: they return []int indices to KEEP,
// perform NO mutation, and emit NO CCR markers. DetectRareStatusValues is the
// BUG#3-FIX core — cardinality is capped at 50 and there is NO >10-distinct
// short-circuit. Per Decision B, the scalar stringifier need only be internally
// consistent within one call, not byte-identical to Python.

// Outlier-detection thresholds [ref: outliers.rs constants].
const (
	// minItemsForOutlierDetection guards DetectStructuralOutliers.
	minItemsForOutlierDetection = 5
	// commonFieldRatio: a field is common when count >= n*commonFieldRatio (>=).
	commonFieldRatio = 0.8
	// rareFieldRatio: a field is rare when count < n*rareFieldRatio (strict <).
	// A field at exactly 20% is neither common nor rare.
	rareFieldRatio = 0.2
	// cardinalityMin / cardinalityMax bound the distinct NON-NULL value count of a
	// common field considered for rare-status detection (BUG#3: max raised to 50,
	// no >10-distinct short-circuit). Inclusive on both ends.
	cardinalityMin = 2
	cardinalityMax = 50
	// paretoCoverage: the dominant cluster must cover >= ceil(total*0.8) values.
	paretoCoverage = 0.8
	// maxTopK: if more than 5 values are needed to reach coverage, the field is
	// skipped (strict >5 -> continue; K<=5 allowed).
	maxTopK = 5
	// nullSentinel is the value-counts key standing in for a null value.
	nullSentinel = "__none__"
)

// DetectStructuralOutliers returns the ascending-sorted, deduplicated indices of
// items that look structurally anomalous: items carrying a rare field, plus items
// carrying a rare status value on a common field [ref: outliers.rs
// detect_structural_outliers]. Non-object (nil) elements are skipped in field
// scans but count toward the denominator n, which shifts the common/rare
// thresholds. An array shorter than 5 yields no outliers.
func DetectStructuralOutliers(items []*orderedmap.OrderedMap) []int {
	// STEP1: too-few short-circuit before any counting.
	if len(items) < minItemsForOutlierDetection {
		return []int{}
	}

	// STEP2: field_counts — +1 per KEY for each object item.
	fieldCounts := map[string]int{}
	for _, item := range items {
		if item == nil {
			continue
		}
		for _, k := range item.Keys() {
			fieldCounts[k]++
		}
	}

	// STEP3: n is the FULL array length (non-objects included).
	n := len(items)
	commonThreshold := float64(n) * commonFieldRatio
	rareThreshold := float64(n) * rareFieldRatio

	// STEP4/STEP5: split into common (>=) and rare (strict <) fields. A field at
	// exactly 20% falls into neither bucket.
	commonFields := map[string]struct{}{}
	rareFields := map[string]struct{}{}
	for name, count := range fieldCounts {
		if float64(count) >= commonThreshold {
			commonFields[name] = struct{}{}
		}
		if float64(count) < rareThreshold {
			rareFields[name] = struct{}{}
		}
	}

	// STEP6/STEP7: collect indices of items carrying any rare field into a set
	// (deduplicated by construction).
	outliers := map[int]struct{}{}
	for i, item := range items {
		if item == nil {
			continue
		}
		for _, k := range item.Keys() {
			if _, ok := rareFields[k]; ok {
				outliers[i] = struct{}{}
				break
			}
		}
	}

	// STEP8: fold in the rare-status indices (dup-prone; the set dedupes them).
	for _, i := range DetectRareStatusValues(items, commonFields) {
		outliers[i] = struct{}{}
	}

	// STEP9: return ascending-sorted.
	result := make([]int, 0, len(outliers))
	for i := range outliers {
		result = append(result, i)
	}
	sort.Ints(result)
	return result
}

// DetectRareStatusValues returns the indices of items whose value on a common
// field falls OUTSIDE that field's Pareto-dominant cluster [ref: outliers.rs
// detect_rare_status_values]. The result is APPEND-ordered (scan order) and may
// contain duplicate indices across fields; the caller (DetectStructuralOutliers)
// deduplicates. A field qualifies only when its distinct non-null values number
// between 2 and 50 inclusive and no more than 5 values are needed to reach 80%
// coverage.
func DetectRareStatusValues(items []*orderedmap.OrderedMap, commonFields map[string]struct{}) []int {
	// STEP A: append-ordered result.
	outlierIndices := []int{}

	// STEP B: iterate common fields in ASCENDING name order for determinism.
	names := make([]string, 0, len(commonFields))
	for name := range commonFields {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, fieldName := range names {
		// STEP C: collect the field's values across objects that carry it, in item
		// order. Missing keys are skipped.
		type fieldValue struct {
			idx  int
			raw  any
			null bool
		}
		var values []fieldValue
		for i, item := range items {
			if item == nil {
				continue
			}
			v, ok := item.Get(fieldName)
			if !ok {
				continue
			}
			values = append(values, fieldValue{idx: i, raw: v, null: v == nil})
		}

		// STEP D/E: distinct NON-NULL stringified values (nulls excluded from
		// cardinality).
		uniqueValues := map[string]struct{}{}
		for _, fv := range values {
			if fv.null {
				continue
			}
			uniqueValues[stringifyOutlierValue(fv.raw)] = struct{}{}
		}

		// STEP F: cardinality gate BEFORE building value_counts. Short-circuits the
		// 95-ok+5-null case (distinct=1) before nulls could be counted.
		if len(uniqueValues) < cardinalityMin || len(uniqueValues) > cardinalityMax {
			continue
		}

		// STEP G: value_counts over ALL values; nulls counted as the sentinel.
		valueCounts := map[string]int{}
		for _, fv := range values {
			key := nullSentinel
			if !fv.null {
				key = stringifyOutlierValue(fv.raw)
			}
			valueCounts[key]++
		}

		// STEP H: empty value_counts -> nothing to do.
		if len(valueCounts) == 0 {
			continue
		}

		// STEP I: total value population.
		total := len(values)

		// STEP J: Pareto order — count DESC, ties broken by key ASC.
		type kv struct {
			key   string
			count int
		}
		ordered := make([]kv, 0, len(valueCounts))
		for k, c := range valueCounts {
			ordered = append(ordered, kv{key: k, count: c})
		}
		sort.SliceStable(ordered, func(a, b int) bool {
			if ordered[a].count != ordered[b].count {
				return ordered[a].count > ordered[b].count
			}
			return ordered[a].key < ordered[b].key
		})

		// STEP K: coverage threshold = ceil(total*0.8).
		threshold := int(math.Ceil(float64(total) * paretoCoverage))

		// STEP L: accumulate the dominant cluster until it covers the threshold.
		topKValues := map[string]struct{}{}
		cumulative := 0
		for _, e := range ordered {
			topKValues[e.key] = struct{}{}
			cumulative += e.count
			if cumulative >= threshold {
				break
			}
		}

		// STEP M: if more than 5 values were needed, skip this field.
		if len(topKValues) > maxTopK {
			continue
		}

		// STEP N: flag items whose value is outside the dominant cluster.
		for _, fv := range values {
			key := nullSentinel
			if !fv.null {
				key = stringifyOutlierValue(fv.raw)
			}
			if _, ok := topKValues[key]; !ok {
				outlierIndices = append(outlierIndices, fv.idx)
			}
		}
	}

	// STEP O: unsorted, dup-prone.
	return outlierIndices
}

// DetectErrorItemsForPreservation returns the ascending indices of object items
// whose lowercased JSON representation contains any ErrorKeywords substring
// [ref: outliers.rs detect_error_items_for_preservation]. When itemStrings is
// provided and long enough, the cached string for an index is used verbatim
// (lowercased); otherwise the item is freshly serialized. Non-object (nil)
// elements are skipped BEFORE the cache lookup.
func DetectErrorItemsForPreservation(items []*orderedmap.OrderedMap, itemStrings []string) []int {
	// STEP P: error indices.
	errorIndices := []int{}

	for i, item := range items {
		// STEP Q: non-object items are skipped before touching the cache.
		if item == nil {
			continue
		}

		// STEP R: use the cache when present and in range, else fresh-serialize.
		var serialized string
		if itemStrings != nil && i < len(itemStrings) {
			serialized = itemStrings[i]
		} else {
			serialized = stringifyOutlierValue(item)
		}
		serialized = strings.ToLower(serialized)

		// STEP S: append the index once if any keyword is a substring.
		for _, kw := range ErrorKeywords {
			if strings.Contains(serialized, kw) {
				errorIndices = append(errorIndices, i)
				break
			}
		}
	}

	// STEP T: return in scan (ascending) order.
	return errorIndices
}

// stringifyOutlierValue renders a JSON value to a stable string key used for
// rarity, value_counts, and per-item lookups [ref: outliers.rs STEP D / goPort
// notes]. Per Decision B it need only be internally consistent within one call,
// not byte-identical to Python. Bools become true/false, numbers their literal
// form, strings their raw text, nulls the sentinel (relevant only where nulls are
// counted), and arrays/objects their compact JSON. The SAME function serves
// rarity, value_counts, and per-item comparison so keys always match.
func stringifyOutlierValue(v any) string {
	switch t := v.(type) {
	case nil:
		return nullSentinel
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case string:
		return t
	default:
		// Arrays ([]any) and objects (*orderedmap.OrderedMap) marshal to compact
		// JSON; orderedmap preserves key order. A marshal failure is a programmer
		// error on decodeJSON-shaped input, so fall back to the sentinel.
		b, err := json.Marshal(v)
		if err != nil {
			return nullSentinel
		}
		return string(b)
	}
}
