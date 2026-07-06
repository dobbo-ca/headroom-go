package smartcrusher

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// Index-set orchestration helpers [ref: orchestration.rs]. These operate over a
// SORTED, deterministic index set (upstream BTreeSet<usize>) modeled here as
// map[int]struct{} materialized via sortedIntSet. They implement content-dedup,
// diversity fill, and critical-first prioritization. They emit NO CCR markers, NO
// savings gate, and NO fallback chain — those live in crusher.go/planning.go.
//
// The content hash is the SAME MD5[:16] over sort_keys json.dumps that
// anchor_selector uses, so cross-helper dedup is consistent.

const (
	// firstAnchorCount / lastAnchorCount are the head/tail anchor spans applied in
	// the over-budget branch of PrioritizeIndices [ref: orchestration.rs].
	firstAnchorCount = 3
	lastAnchorCount  = 2
	// contentHashHexLen is the MD5-prefix width shared with anchor_selector.
	contentHashHexLen = 16
	// nullScalarLiteral is the stand-in serialized form of a JSON null in the scalar
	// hash branch (matches the object-serializer's None rendering).
	nullScalarLiteral = "None"
)

// sortedIntSet materializes an index set as an ascending []int. Go map iteration
// order is nondeterministic, so every consumer of an int set that must be stable
// funnels through here [ref: orchestration.rs goPortNotes — never iterate a Go map
// unsorted].
func sortedIntSet(set map[int]struct{}) []int {
	out := make([]int, 0, len(set))
	for i := range set {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// cloneIntSet returns an independent copy of an int set.
func cloneIntSet(set map[int]struct{}) map[int]struct{} {
	out := make(map[int]struct{}, len(set))
	for i := range set {
		out[i] = struct{}{}
	}
	return out
}

// satSub is a saturating subtraction on non-negative ints (no underflow).
func satSub(a, b int) int {
	if b >= a {
		return 0
	}
	return a - b
}

// itemContentHash returns the dedup key for one array item [ref: orchestration.rs
// item_content_hash]. Objects and arrays hash via computeItemHash (canonical
// sort-keys serialization); scalars hash their string form directly:
// String -> raw, Number -> string token, Bool -> "true"/"false", Null -> "None"
// literal; a serialization failure falls back to "__idx_<idx>__". All forms are
// MD5'd and truncated to 16 hex chars.
func itemContentHash(item any, idx int) string {
	switch v := item.(type) {
	case *orderedmap.OrderedMap, []any:
		s, ok := computeItemHashSerialize(v)
		if !ok {
			return md5Hex16(fmt.Sprintf("__idx_%d__", idx))
		}
		return md5Hex16(s)
	case nil:
		return md5Hex16(nullScalarLiteral)
	case string:
		return md5Hex16(v)
	case bool:
		if v {
			return md5Hex16("true")
		}
		return md5Hex16("false")
	default:
		// json.Number (and any other scalar) -> its string form.
		if s, ok := scalarString(v); ok {
			return md5Hex16(s)
		}
		return md5Hex16(fmt.Sprintf("__idx_%d__", idx))
	}
}

// scalarString renders a non-container scalar to its string key form (Decision B:
// internally consistent, not byte-parity with Python).
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case nil:
		return nullScalarLiteral, true
	default:
		return canonicalScalar(t)
	}
}

// md5Hex16 returns the lowercase hex of the first 16 chars of the MD5 of s.
func md5Hex16(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:contentHashHexLen]
}

// computeItemHashSerialize renders an object/array value with sort_keys=true,
// separators (", ", ": "), and ensure_ascii=true (non-ASCII escaped to \uXXXX),
// mirroring Python's json.dumps used by compute_item_hash. Object keys are sorted
// ASCII-ascending before rendering so {b,a} and {a,b} hash identically.
func computeItemHashSerialize(v any) (string, bool) {
	var sb strings.Builder
	if !canonicalWrite(&sb, v) {
		return "", false
	}
	return sb.String(), true
}

// canonicalWrite writes v in canonical (sort-keys, spaced-separator, ascii-escaped)
// form. It returns false if a value type cannot be serialized.
func canonicalWrite(sb *strings.Builder, v any) bool {
	switch t := v.(type) {
	case *orderedmap.OrderedMap:
		keys := append([]string(nil), t.Keys()...)
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			writeCanonicalString(sb, k)
			sb.WriteString(": ")
			val, _ := t.Get(k)
			if !canonicalWrite(sb, val) {
				return false
			}
		}
		sb.WriteByte('}')
		return true
	case []any:
		sb.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				sb.WriteString(", ")
			}
			if !canonicalWrite(sb, e) {
				return false
			}
		}
		sb.WriteByte(']')
		return true
	case string:
		writeCanonicalString(sb, t)
		return true
	case bool:
		if t {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
		return true
	case nil:
		sb.WriteString("null")
		return true
	default:
		s, ok := canonicalScalar(t)
		if !ok {
			return false
		}
		sb.WriteString(s)
		return true
	}
}

// canonicalScalar renders a numeric scalar (json.Number) as its literal token.
func canonicalScalar(v any) (string, bool) {
	if n, ok := v.(json.Number); ok {
		return n.String(), true
	}
	return "", false
}

// writeCanonicalString writes a JSON string literal with ensure_ascii semantics:
// non-ASCII runes are escaped to \uXXXX (surrogate pairs for astral planes).
func writeCanonicalString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				sb.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else if r < 0x80 {
				sb.WriteByte(byte(r))
			} else if r <= 0xFFFF {
				sb.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				// Astral plane: encode as a UTF-16 surrogate pair.
				r -= 0x10000
				hi := 0xD800 + (r >> 10)
				lo := 0xDC00 + (r & 0x3FF)
				sb.WriteString(fmt.Sprintf(`\u%04x\u%04x`, hi, lo))
			}
		}
	}
	sb.WriteByte('"')
}

// DeduplicateIndicesByContent collapses content-duplicate indices to the lowest
// index [ref: orchestration.rs deduplicate_indices_by_content]. Iterating the input
// set ascending, it records the first index seen for each content hash, skipping
// out-of-bounds indices. An empty input yields an empty set.
func DeduplicateIndicesByContent(keepIndices map[int]struct{}, items []any) map[int]struct{} {
	result := make(map[int]struct{})
	if len(keepIndices) == 0 {
		return result
	}
	seen := make(map[string]struct{})
	for _, idx := range sortedIntSet(keepIndices) {
		if idx < 0 || idx >= len(items) {
			continue
		}
		h := itemContentHash(items[idx], idx)
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		result[idx] = struct{}{}
	}
	return result
}

// FillRemainingSlots tops the keep set up toward effectiveMax with content-unique,
// stride-sampled candidates [ref: orchestration.rs fill_remaining_slots]. When the
// set is already at/over budget it is returned unchanged. Candidates are the
// in-bounds indices NOT already kept, walked in a stride pattern so the fill is
// spread across the array rather than clustered at the head.
func FillRemainingSlots(keepIndices map[int]struct{}, items []any, n, effectiveMax int) map[int]struct{} {
	result := cloneIntSet(keepIndices)
	remaining := satSub(effectiveMax, len(keepIndices))
	if remaining == 0 {
		return result
	}

	seen := make(map[string]struct{})
	for idx := range keepIndices {
		if idx >= 0 && idx < n {
			seen[itemContentHash(items[idx], idx)] = struct{}{}
		}
	}

	candidates := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if _, kept := keepIndices[i]; !kept {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return result
	}

	step := len(candidates) / (remaining + 1)
	if step < 1 {
		step = 1
	}

	added := 0
	for startOffset := 0; startOffset < step; startOffset++ {
		if added >= remaining {
			break
		}
		for i := startOffset; i < len(candidates); i += step {
			if added >= remaining {
				break
			}
			idx := candidates[i]
			h := itemContentHash(items[idx], idx)
			if _, dup := seen[h]; dup {
				continue
			}
			result[idx] = struct{}{}
			seen[h] = struct{}{}
			added++
		}
	}
	return result
}

// PrioritizeIndices resolves the final keep set under a budget [ref:
// orchestration.rs prioritize_indices]. It dedups (when enabled), fills toward the
// budget, and returns early if the result is within budget. Over budget, it keeps
// ALL critical rows (errors, structural outliers, numeric anomalies) plus the
// first-3/last-2 anchors plus remaining fill — the result MAY exceed effectiveMax
// because criticals are never dropped. analysis may be nil.
func PrioritizeIndices(cfg *Config, keepIndices map[int]struct{}, items []any, n int, analysis *ArrayAnalysis, effectiveMax int) map[int]struct{} {
	// STEP1: dedup (or clone).
	var current map[int]struct{}
	if cfg.DedupIdenticalItems {
		current = DeduplicateIndicesByContent(keepIndices, items)
	} else {
		current = cloneIntSet(keepIndices)
	}

	// STEP2: fill when under budget and under n.
	if len(current) < effectiveMax && len(current) < n {
		current = FillRemainingSlots(current, items, n, effectiveMax)
	}

	// STEP3: within budget -> return.
	if len(current) <= effectiveMax {
		return current
	}

	// OVER BUDGET: union of criticals + anchors + fill-others.
	objs := asOrderedMaps(items)
	prioritized := make(map[int]struct{})
	for _, idx := range DetectErrorItemsForPreservation(objs, nil) {
		if idx >= 0 && idx < n {
			prioritized[idx] = struct{}{}
		}
	}
	for _, idx := range DetectStructuralOutliers(objs) {
		if idx >= 0 && idx < n {
			prioritized[idx] = struct{}{}
		}
	}
	for idx := range numericAnomalyIndices(cfg, items, analysis) {
		prioritized[idx] = struct{}{}
	}
	// learned_indices hardwired EMPTY (TOIN DEFERRED).

	// ANCHORS: first-3 then last-2, each gated on remaining budget.
	remaining := satSub(effectiveMax, len(prioritized))
	if remaining > 0 {
		firstEnd := firstAnchorCount
		if firstEnd > n {
			firstEnd = n
		}
		for i := 0; i < firstEnd; i++ {
			if remaining == 0 {
				break
			}
			if _, ok := prioritized[i]; !ok {
				prioritized[i] = struct{}{}
				remaining--
			}
		}
		lastStart := satSub(n, lastAnchorCount)
		for i := lastStart; i < n; i++ {
			if remaining == 0 {
				break
			}
			if _, ok := prioritized[i]; !ok {
				prioritized[i] = struct{}{}
				remaining--
			}
		}
	}

	// FILL-OTHERS: remaining slots from the current (post-dedup/fill) set, ascending.
	if remaining > 0 {
		others := make([]int, 0, len(current))
		for idx := range current {
			if _, ok := prioritized[idx]; !ok {
				others = append(others, idx)
			}
		}
		sort.Ints(others)
		for _, idx := range others {
			if remaining == 0 {
				break
			}
			prioritized[idx] = struct{}{}
			remaining--
		}
	}
	return prioritized
}

// numericAnomalyIndices flags items whose numeric field values fall outside
// mean ± variance_threshold*std [ref: orchestration.rs numeric_anomaly_indices].
// A nil analysis or empty field_stats yields an empty set. Non-object items,
// missing fields, non-numeric values, and NaN are skipped; variance<=0 / std<=0
// short-circuits the whole field.
func numericAnomalyIndices(cfg *Config, items []any, analysis *ArrayAnalysis) map[int]struct{} {
	result := make(map[int]struct{})
	if analysis == nil || analysis.FieldStats == nil {
		return result
	}
	objs := asOrderedMaps(items)
	for _, name := range analysis.FieldStats.Keys() {
		v, _ := analysis.FieldStats.Get(name)
		stats, ok := v.(FieldStats)
		if !ok {
			continue
		}
		if stats.FieldType != "numeric" || stats.MeanVal == nil || stats.Variance == nil {
			continue
		}
		if *stats.Variance <= 0 {
			continue
		}
		std := math.Sqrt(*stats.Variance)
		if std <= 0 {
			continue
		}
		threshold := cfg.VarianceThreshold * std
		mean := *stats.MeanVal
		for i, obj := range objs {
			if obj == nil {
				continue
			}
			raw, present := obj.Get(name)
			if !present {
				continue
			}
			num, okNum := asFloat64(raw)
			if !okNum {
				continue
			}
			if math.Abs(num-mean) > threshold {
				result[i] = struct{}{}
			}
		}
	}
	return result
}

// asOrderedMaps projects []any to []*orderedmap.OrderedMap, mapping any non-object
// element to a nil entry so index alignment with the source slice is preserved.
func asOrderedMaps(items []any) []*orderedmap.OrderedMap {
	out := make([]*orderedmap.OrderedMap, len(items))
	for i, it := range items {
		if om, ok := it.(*orderedmap.OrderedMap); ok {
			out[i] = om
		}
	}
	return out
}
