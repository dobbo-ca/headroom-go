package smartcrusher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher/compaction"
	"github.com/iancoleman/orderedmap"
)

// errNotArray is returned by CrushArrayJSON when the decoded value is not an array.
var errNotArray = errors.New("smartcrusher: input JSON is not an array")

// This file is THE CROWN [ref: crusher.rs]: the top-level SmartCrusher entry that
// implements the offloads.Crusher seam and orchestrates the 3-tier crush funnel.
// The SmartCrusher struct and fromParts live in builder.go (Build() materializes
// them); the crush behavior lives here. It wires together every other module but
// owns no statistics/planning logic of its own beyond the funnel + walker.
//
// Two hashes, per the plan's opaque-cell + CCR contracts:
//   - Tier-3 lossy space-form marker: hashCanonical (SHA-256 first 6 bytes -> 12
//     hex) over the canonical serialized slice; store-written, gated on
//     EnableCCRMarker; round-trippable.
//   - processString opaque branch: delegates to the walker's EmitOpaqueCCRMarker
//     (BLAKE3 24-hex, store-writing) via a DocumentCompactor carrying ccrStore.
//   - Tier-2 lossless-table opaque CELLS: 12-hex hashOpaque from the compactor,
//     NOT store-written / NOT round-trippable in the MVP (byte-saving only).

// maxProcessDepth is the recursion guard: past this depth processValue returns the
// value unchanged (no panic on pathologically nested input) [ref: crusher.rs
// MAX_PROCESS_DEPTH].
const maxProcessDepth = 50

// NewSmartCrusher builds the lossless-first OSS default crusher: the two default
// constraints + tracing observer, the CSV-schema compaction stage (Tier-2), and an
// in-memory CCR store for the lossy Tier-3 offloads. The blank import in builder.go
// guarantees the in-memory backend is registered, so Build() cannot fail.
func NewSmartCrusher(cfg Config) *SmartCrusher {
	return NewSmartCrusherBuilder(cfg).
		WithDefaultOSSSetup().
		WithDefaultCompaction().
		WithDefaultCCRStore().
		Build()
}

// NewSmartCrusherWithoutCompaction builds a lossy-only crusher (no compaction
// stage), still with an in-memory CCR store for the space-form lossy offloads. It
// forces every crushable array past the lossless tier straight to the lossy funnel.
func NewSmartCrusherWithoutCompaction(cfg Config) *SmartCrusher {
	return NewSmartCrusherBuilder(cfg).
		WithDefaultOSSSetup().
		WithDefaultCCRStore().
		Build()
}

// Crush is the offloads.Crusher seam entry point [ref: crusher.rs Crush]. It runs
// the crush and maps the INTERNAL result onto the 2-field seam type, dropping the
// Strategy/Original fields (do NOT widen the seam).
func (c *SmartCrusher) Crush(content, query string, bias float64) offloads.CrushResult {
	compressed, wasModified, _ := c.smartCrushContent(content, query, bias)
	return offloads.CrushResult{Compressed: compressed, WasModified: wasModified}
}

// smartCrushContent parses content as ordered JSON, walks it with processValue at
// depth 0, and re-serializes compact [ref: crusher.rs smartCrushContent]. A parse
// error passes the content through unchanged (crushed=content, wasModified=false,
// info=""). wasModified compares the compact result against TrimSpace(content), so
// a whitespace-only reformat (e.g. "[1, 2, 3]" -> "[1,2,3]") counts as modified.
func (c *SmartCrusher) smartCrushContent(content, queryContext string, bias float64) (crushed string, wasModified bool, info string) {
	parsed, err := decodeJSON(content)
	if err != nil {
		return content, false, ""
	}
	processed, resultInfo := c.processValue(parsed, 0, queryContext, bias)
	result := pythonSafeJSONDumps(processed)
	return result, result != strings.TrimSpace(content), resultInfo
}

// processValue is the recursive walker [ref: crusher.rs processValue]. Past
// maxProcessDepth it returns the value unchanged. It dispatches on the decodeJSON
// shape: arrays route to the classifier + crushArray (DictArray) or recurse
// element-wise; objects recurse key-wise (second-pass crushObject DEFERRED); strings
// route to processString; other scalars are returned unchanged.
func (c *SmartCrusher) processValue(value any, depth int, queryContext string, bias float64) (any, string) {
	if depth >= maxProcessDepth {
		return value, ""
	}

	switch v := value.(type) {
	case []any:
		return c.processArray(v, depth, queryContext, bias)
	case *orderedmap.OrderedMap:
		return c.processObject(v, depth, queryContext, bias)
	case string:
		return c.processString(v, depth, queryContext, bias)
	default:
		return value, ""
	}
}

// processArray handles the array arm of processValue. Arrays of length >=
// MinItemsToAnalyze are classified: a DictArray runs crushArray (with lossless
// substitution or a dropped-row sentinel append); the other homogeneous crushers
// (string/number/mixed) are DEFERRED and fall through to element-wise recursion,
// as do below-threshold and Nested/Bool/Empty arrays.
func (c *SmartCrusher) processArray(items []any, depth int, queryContext string, bias float64) (any, string) {
	if len(items) >= c.config.MinItemsToAnalyze {
		switch ClassifyArray(items) {
		case DictArray:
			r := c.crushArray(items, queryContext, bias)
			if r.Compacted != nil {
				info := r.StrategyInfo + "(" + strconv.Itoa(len(items)) + "->len=" + strconv.Itoa(len(*r.Compacted)) + ")"
				return *r.Compacted, info
			}
			info := r.StrategyInfo + "(" + strconv.Itoa(len(items)) + "->" + strconv.Itoa(len(r.Items)) + ")"
			out := make([]any, len(r.Items))
			copy(out, r.Items)
			if r.DroppedSummary != "" {
				sentinel := orderedmap.New()
				sentinel.Set("_ccr_dropped", r.DroppedSummary)
				out = append(out, sentinel)
			}
			return out, info
		case StringArray, NumberArray, MixedArray:
			// DEFERRED (Plan 4+): crushStringArray / crushNumberArray / crushMixedArray
			// depend on compute_optimal_k (Kneedle/SimHash/zlib). Fall through to
			// element-wise recursion so the value is walked but not crushed.
		}
	}

	// Below threshold / non-crushable: recurse element-wise, joining sub-infos.
	out := make([]any, len(items))
	parts := make([]string, 0, len(items))
	for i, item := range items {
		p, pInfo := c.processValue(item, depth+1, queryContext, bias)
		out[i] = p
		if pInfo != "" {
			parts = append(parts, pInfo)
		}
	}
	return out, strings.Join(parts, ",")
}

// processObject handles the object arm of processValue [ref: crusher.rs processValue
// Object]. FIRST PASS recurses each value in insertion order. The SECOND PASS
// object-level crush (crushObject) is DEFERRED, so the recursed object is returned.
func (c *SmartCrusher) processObject(om *orderedmap.OrderedMap, depth int, queryContext string, bias float64) (any, string) {
	out := orderedmap.New()
	parts := make([]string, 0, len(om.Keys()))
	for _, k := range om.Keys() {
		v, _ := om.Get(k)
		p, pInfo := c.processValue(v, depth+1, queryContext, bias)
		out.Set(k, p)
		if pInfo != "" {
			parts = append(parts, pInfo)
		}
	}
	// DEFERRED (Plan 4+): the second-pass crushObject (object-key dedup/factoring)
	// depends on compute_optimal_k; it is stubbed to passthrough, so the recursed
	// object is returned as-is.
	return out, strings.Join(parts, ",")
}

// processString handles a string leaf [ref: crusher.rs processString]. (1) If the
// string parses as a JSON container, recurse into it and re-emit the rendered/
// serialized result with a "string_json"[+subInfo] tag; a String result (a lossless
// sub-table substitution) is used DIRECTLY (never re-quoted). (2) Otherwise, if the
// string classifies Opaque, emit a store-writing comma-form CCR marker via the
// walker (BLAKE3 24-hex). (3) Else return the string unchanged.
func (c *SmartCrusher) processString(s string, depth int, queryContext string, bias float64) (any, string) {
	// (1) JSON-container recursion.
	if parsed, ok := compaction.TryParseJSONContainer(s); ok {
		processed, subInfo := c.processValue(parsed, depth+1, queryContext, bias)
		var rendered string
		if str, isStr := processed.(string); isStr {
			// Lossless substitution already produced a string — use it directly, do
			// NOT JSON-re-encode (would double-quote it).
			rendered = str
		} else {
			rendered = compactSerialize(processed)
		}
		info := "string_json"
		if subInfo != "" {
			info = "string_json[" + subInfo + "]"
		}
		return rendered, info
	}

	// (2) Opaque classification -> store-writing comma-form marker (walker path).
	// A DocumentCompactor carrying ccrStore renders a bare opaque string via
	// EmitOpaqueCCRMarker (BLAKE3 24-hex, store-written); a non-opaque string is
	// returned unchanged. The container case is already handled above, so this only
	// ever classifies or passes through.
	dc := compaction.DocumentCompactor{
		Config:    compaction.DefaultCompactConfig(),
		Formatter: compaction.CsvSchemaFormatter{},
		CcrStore:  c.ccrStore,
	}
	if walked, ok := dc.Compact(s).(string); ok && walked != s {
		return walked, "string_ccr"
	}

	// (3) Plain string.
	return s, ""
}

// CrushArrayResult is the internal result of crushArray [ref: crusher.rs
// CrushArrayResult]. Items is the (possibly reduced) kept slice; Compacted, when
// non-nil, is the rendered lossless-table string that REPLACES the array wholesale;
// CcrHash/DroppedSummary describe a lossy row drop (space-form marker + store hash).
type CrushArrayResult struct {
	Items          []any
	StrategyInfo   string
	CcrHash        *string
	DroppedSummary string
	Compacted      *string
	CompactionKind *string
}

// crushArray is the 3-tier crush funnel [ref: crusher.rs crushArray].
//
//	Tier-1 (passthrough): adaptiveK = (MaxItemsAfterCrush>0 ? min(MaxItemsAfterCrush,n)
//	  : n). If n <= adaptiveK, return a copy unchanged ("none:adaptive_at_limit"). This
//	  NEVER truncates items[0:adaptiveK]; when n exceeds the cap the planner (Tier-3)
//	  chooses kept rows.
//	Tier-2 (lossless): when a compaction stage is present, run it and, if the savings
//	  ratio meets LosslessMinSavingsRatio (BYTES), return the rendered table as
//	  Compacted (nothing dropped, no store write).
//	Tier-3 (lossy): analyze; a Skip verdict short-circuits to keep-all; otherwise plan
//	  + execute, drop rows, and (gated on EnableCCRMarker) write a space-form CCR marker
//	  + stash the canonical original slice.
func (c *SmartCrusher) crushArray(items []any, queryContext string, bias float64) CrushArrayResult {
	n := len(items)

	// Step1: per-item compact serializations (used by the estimate + the planner).
	itemStrings := make([]string, n)
	for i, it := range items {
		itemStrings[i] = compactSerialize(it)
	}

	// Step2: adaptive K (MVP simplified — passthrough limit only, NOT a truncation).
	// DEFERRED (Plan 4+): computeOptimalK (SimHash dedup + Kneedle elbow + zlib).
	adaptiveK := n
	if c.config.MaxItemsAfterCrush > 0 {
		adaptiveK = c.config.MaxItemsAfterCrush
		if n < adaptiveK {
			adaptiveK = n
		}
	}

	// Tier-1: passthrough when at or under the adaptive limit (inclusive).
	if n <= adaptiveK {
		return CrushArrayResult{
			Items:        cloneAnySlice(items),
			StrategyInfo: "none:adaptive_at_limit",
		}
	}

	// Tier-2: lossless compaction table (gated on the savings ratio).
	if c.compaction != nil {
		comp, rendered := c.compaction.Run(items)
		if comp != nil && (*comp).WasCompacted() {
			inputBytes := estimateArrayBytes(itemStrings)
			savingsRatio := 0.0
			if inputBytes > 0 {
				savingsRatio = 1.0 - float64(len(rendered))/float64(inputBytes)
			}
			if savingsRatio >= c.config.LosslessMinSavingsRatio {
				kind := compactionKindStr(*comp)
				return CrushArrayResult{
					Items:          cloneAnySlice(items),
					StrategyInfo:   "lossless:" + kind,
					Compacted:      &rendered,
					CompactionKind: &kind,
				}
			}
		}
	}

	// Tier-3: lossy sampling. effectiveMaxItems == adaptiveK, which at this point
	// (reached only when n > adaptiveK) equals MaxItemsAfterCrush exactly — the
	// min() picked the cap because n exceeds it — so this matches both the ref
	// (effectiveMaxItems=adaptiveK) and the plan's "equals MaxItemsAfterCrush" note.
	effectiveMaxItems := adaptiveK
	objs := asOrderedMaps(items)
	analysis := c.analyzer.AnalyzeArray(objs)

	if analysis.RecommendedStrategy == StrategySkip {
		reason := ""
		if analysis.Crushability != nil {
			reason = "skip:" + analysis.Crushability.Reason
		}
		return CrushArrayResult{
			Items:        cloneAnySlice(items),
			StrategyInfo: reason,
		}
	}

	plan := c.planner.CreatePlan(&analysis, objs, queryContext, nil, &effectiveMaxItems, itemStrings)
	result := c.executePlan(plan, items)

	// CCR marker: only when rows were actually dropped.
	droppedCount := satSub(n, len(result))
	var ccrHash *string
	droppedSummary := ""
	if droppedCount > 0 && c.config.EnableCCRMarker {
		canonical := compactSerialize(items)
		h := hashCanonical(canonical)
		droppedSummary = ccr.MarkerForLossy(h, droppedCount)
		if c.ccrStore != nil {
			c.ccrStore.Put(h, canonical)
		}
		ccrHash = &h
	}

	return CrushArrayResult{
		Items:          result,
		StrategyInfo:   analysis.RecommendedStrategy.String(),
		CcrHash:        ccrHash,
		DroppedSummary: droppedSummary,
	}
}

// executePlan filters items to the plan's keep indices [ref: crusher.rs executePlan]:
// sort the kept indices ascending, drop out-of-bounds, and deep-copy the kept items
// in sorted order. The factor_out_constants strip is gated (default OFF); when active
// it removes keys whose value equals the recorded constant and prepends a
// {"_constant_fields":{...}} sentinel iff at least one key was stripped.
func (c *SmartCrusher) executePlan(plan *CompressionPlan, items []any) []any {
	indices := append([]int(nil), plan.KeepIndices...)
	sort.Ints(indices)

	kept := make([]any, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(items) {
			continue
		}
		kept = append(kept, cloneAnyValue(items[idx]))
	}

	// factor_out_constants (default OFF). Defensive strip: only removes a key whose
	// value EQUALS the recorded constant; drifted values are kept.
	if c.config.FactorOutConstants && plan.ConstantFields != nil && len(plan.ConstantFields.Keys()) > 0 && len(kept) >= 2 {
		anyStripped := false
		constKeys := append([]string(nil), plan.ConstantFields.Keys()...)
		sort.Strings(constKeys) // BTreeMap sorted-key iteration.
		for _, item := range kept {
			obj, ok := item.(*orderedmap.OrderedMap)
			if !ok {
				continue
			}
			for _, key := range constKeys {
				constVal, _ := plan.ConstantFields.Get(key)
				cur, present := obj.Get(key)
				if present && jsonEqual(cur, constVal) {
					obj.Delete(key)
					anyStripped = true
				}
			}
		}
		if anyStripped {
			sentinel := orderedmap.New()
			cf := orderedmap.New()
			for _, key := range constKeys {
				v, _ := plan.ConstantFields.Get(key)
				cf.Set(key, v)
			}
			sentinel.Set("_constant_fields", cf)
			kept = append([]any{any(sentinel)}, kept...)
		}
	}

	return kept
}

// CcrGet retrieves a stored CCR payload by hash (test/round-trip helper). It returns
// ("", false) when the store is nil or the hash is absent.
func (c *SmartCrusher) CcrGet(hash string) (string, bool) {
	if c.ccrStore == nil {
		return "", false
	}
	return c.ccrStore.Get(hash)
}

// CcrLen reports the CCR store entry count (0 when the store is nil).
func (c *SmartCrusher) CcrLen() int {
	if c.ccrStore == nil {
		return 0
	}
	return c.ccrStore.Len()
}

// CrushArrayJSON is a test helper: decode a JSON array string, run crushArray, and
// return the raw CrushArrayResult.
func (c *SmartCrusher) CrushArrayJSON(arrayJSON, queryContext string, bias float64) (CrushArrayResult, error) {
	v, err := decodeJSON(arrayJSON)
	if err != nil {
		return CrushArrayResult{}, err
	}
	arr, ok := v.([]any)
	if !ok {
		return CrushArrayResult{}, errNotArray
	}
	return c.crushArray(arr, queryContext, bias), nil
}

// CompactDocumentJSON drives the DocumentCompactor walker path with the crusher's
// ccrStore, so opaque blobs it emits ARE store-written and round-trippable (BLAKE3
// 24-hex). It decodes doc, walks it, and re-serializes the result compact. Used by
// the parity round-trip fixtures for the walker opaque path.
func (c *SmartCrusher) CompactDocumentJSON(doc string) (string, error) {
	parsed, err := decodeJSON(doc)
	if err != nil {
		return "", err
	}
	dc := compaction.DocumentCompactor{
		Config:    compaction.DefaultCompactConfig(),
		Formatter: compaction.CsvSchemaFormatter{},
		CcrStore:  c.ccrStore,
	}
	walked := dc.Compact(parsed)
	if s, ok := walked.(string); ok {
		return s, nil
	}
	return compactSerialize(walked), nil
}

// hashCanonical is the Tier-3 lossy space-form hash: SHA-256 first 6 bytes -> 12
// lowercase hex chars over the canonical serialized BYTES [ref: crusher.rs
// hashCanonical]. Decision B: a single stable Go serializer feeds it consistently
// (this is a cache key, not a cross-language-identical hash).
func hashCanonical(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:6])
}

// compactionKindStr labels a Compaction for the "lossless:<kind>" strategy string.
func compactionKindStr(comp compaction.Compaction) string {
	switch comp.(type) {
	case compaction.Table:
		return "table"
	case compaction.Buckets:
		return "buckets"
	case compaction.OpaqueRef:
		return "opaque"
	default:
		return "table"
	}
}

// estimateArrayBytes estimates the byte size of a JSON array from its per-item
// serialized strings [ref: crusher.rs estimate_array_bytes]: sum(len(itemStr)) +
// (n-1 saturating, for commas) + 2 (brackets). BYTE lengths (porting rule 2).
func estimateArrayBytes(itemStrings []string) int {
	total := 0
	for _, s := range itemStrings {
		total += len(s)
	}
	return total + satSub(len(itemStrings), 1) + 2
}

// cloneAnySlice makes a deep copy of a []any (each element deep-copied).
func cloneAnySlice(items []any) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = cloneAnyValue(it)
	}
	return out
}

// cloneAnyValue deep-copies a decodeJSON-shaped value so downstream mutation
// (executePlan's factor-out strip) never touches the original items.
func cloneAnyValue(v any) any {
	switch val := v.(type) {
	case *orderedmap.OrderedMap:
		out := orderedmap.New()
		for _, k := range val.Keys() {
			inner, _ := val.Get(k)
			out.Set(k, cloneAnyValue(inner))
		}
		return out
	case []any:
		return cloneAnySlice(val)
	default:
		// json.Number, string, bool, nil are immutable — share directly.
		return v
	}
}

// jsonEqual reports structural equality of two decodeJSON-shaped values (used by the
// factor-out-constants defensive strip).
func jsonEqual(a, b any) bool {
	return compactSerialize(a) == compactSerialize(b)
}
