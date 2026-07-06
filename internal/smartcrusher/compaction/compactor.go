package compaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/iancoleman/orderedmap"
)

// This file is the tabular compactor: the heart of the lossless compaction-table
// build [ref: compaction/compactor.rs]. It turns an array of objects into a
// Compaction IR (Untouched | Table). The heterogeneous branch
// (detect_discriminator + bucket_by + Buckets) is DEFERRED for the MVP — the
// core_ratio is still computed so the seam is honest, but the compactor always
// falls through to buildHomogeneousTable (upstream itself falls through when no
// discriminator is found). No float-parity concerns live here (Decision B).

// CompactConfig tunes the tabular compaction. Classify is threaded down to
// cellFromValue for the opaque-cell decision.
type CompactConfig struct {
	// Classify is the per-cell classifier config (opaque thresholds, etc.).
	Classify ClassifyConfig
	// MinItems is the minimum array length to attempt compaction (< this ->
	// Untouched).
	MinItems int
	// CoreFieldFraction sets the core threshold: a key is core when its row
	// frequency >= ceil(total * CoreFieldFraction).
	CoreFieldFraction float64
	// HeterogeneousCoreRatio is the DEFERRED discriminator gate (core_ratio below
	// this would trigger bucketing upstream; the MVP always falls through).
	HeterogeneousCoreRatio float64
	// MaxFlattenInnerKeys caps the inner key count a uniform nested object may have
	// to be dotted-flattened.
	MaxFlattenInnerKeys int
	// MinBuckets/MaxBuckets bound the DEFERRED bucketing path.
	MinBuckets int
	MaxBuckets int
}

// DefaultCompactConfig returns the upstream defaults [ref: (d) COMPACTION].
func DefaultCompactConfig() CompactConfig {
	return CompactConfig{
		Classify:               DefaultClassifyConfig(),
		MinItems:               2,
		CoreFieldFraction:      0.8,
		HeterogeneousCoreRatio: 0.6,
		MaxFlattenInnerKeys:    6,
		MinBuckets:             2,
		MaxBuckets:             8,
	}
}

// nestedRecurseMinLen is the minimum array-of-objects length a cell must have to
// recurse into a Nested compaction [ref: compactor.rs constants].
const nestedRecurseMinLen = 2

// Compact turns an array of decodeJSON-shaped items into a Compaction IR. It
// declines (Untouched) when the array is too short or any element is not an
// object. The heterogeneous discriminator path is DEFERRED, so a low core_ratio
// still falls through to the homogeneous table.
func Compact(items []any, cfg CompactConfig) Compaction {
	// Step1 untouched gates: too few items, or any non-object element.
	if len(items) < cfg.MinItems {
		return Untouched{Value: items}
	}
	for _, it := range items {
		if _, ok := it.(*orderedmap.OrderedMap); !ok {
			return Untouched{Value: items}
		}
	}

	// Step2 key freqs (BTreeMap-sorted for determinism): DISTINCT rows containing
	// each key.
	freqs := computeKeyFreqs(items)

	// Step3 core ratio (computed for the seam; consumer is DEFERRED).
	total := len(items)
	coreThreshold := int(math.Ceil(float64(total) * cfg.CoreFieldFraction))
	coreCount := 0
	for _, f := range freqs {
		if f.freq >= coreThreshold {
			coreCount++
		}
	}
	totalKeys := len(freqs)
	// core_ratio is computed to keep the discriminator seam honest but is not
	// consumed in the MVP (heterogeneous bucketing is DEFERRED).
	_ = coreRatio(coreCount, totalKeys)

	// Step4 heterogeneous branch is DEFERRED.
	//
	// DEFERRED (Plan 4+): when core_ratio < HeterogeneousCoreRatio, upstream calls
	// detect_discriminator and, on Some, bucket_by -> Buckets. The MVP skips the
	// discriminator entirely and always falls through to the homogeneous table
	// (upstream itself falls through when no discriminator is found).

	// Step5 build the homogeneous table.
	return buildHomogeneousTable(items, freqs, cfg)
}

// coreRatio is the core-field ratio; when there are no keys it is hardcoded 1.0
// [ref: compactor.rs edgeCases].
func coreRatio(coreCount, totalKeys int) float64 {
	if totalKeys == 0 {
		return 1.0
	}
	return float64(coreCount) / float64(totalKeys)
}

// keyFreq pairs a key with its DISTINCT-row frequency.
type keyFreq struct {
	name string
	freq int
}

// computeKeyFreqs counts, for each key, the number of DISTINCT objects that
// contain it. It returns the pairs in ASCII-sorted key order (mirrors the Rust
// BTreeMap) so downstream ordering is deterministic before the DESC-freq sort.
func computeKeyFreqs(items []any) []keyFreq {
	counts := map[string]int{}
	for _, it := range items {
		om, ok := it.(*orderedmap.OrderedMap)
		if !ok {
			continue
		}
		for _, k := range om.Keys() {
			counts[k]++
		}
	}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Strings(names) // ASCII sort mirrors BTreeMap iteration.
	out := make([]keyFreq, len(names))
	for i, n := range names {
		out[i] = keyFreq{name: n, freq: counts[n]}
	}
	return out
}

// buildHomogeneousTable constructs a Table: column union ordered DESC frequency
// (ties ASC name), per-column type inference and nullability, one row per item,
// then a single dotted-flatten pass.
func buildHomogeneousTable(items []any, freqs []keyFreq, cfg CompactConfig) Compaction {
	total := len(items)

	// (A) ordered_keys: DESC frequency, ties ASC name. freqs already ASC-name, so a
	// stable sort by DESC freq keeps the ASC-name tiebreak.
	ordered := make([]keyFreq, len(freqs))
	copy(ordered, freqs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].freq > ordered[j].freq
	})

	orderedKeys := make([]string, len(ordered))
	for i, kf := range ordered {
		orderedKeys[i] = kf.name
	}

	// (B) field specs: type tag + nullable (sparse OR any explicit null).
	fields := make([]FieldSpec, len(ordered))
	for i, kf := range ordered {
		nullable := kf.freq < total || hasExplicitNull(items, kf.name)
		fields[i] = FieldSpec{
			Name:     kf.name,
			TypeTag:  inferTypeTag(items, kf.name),
			Nullable: nullable,
		}
	}
	schema := Schema{Fields: fields}

	// (C) rows: one per item, cells in ordered_keys order.
	rows := make([]Row, len(items))
	for i, it := range items {
		rows[i] = buildRow(it, orderedKeys, cfg)
	}

	// (D) dotted flatten (one level per pass; mutates schema.Fields + rows).
	flattenUniformNested(&schema.Fields, rows, cfg)

	// (E) Table.
	return Table{Schema: schema, Rows: rows, OriginalCount: len(items)}
}

// hasExplicitNull reports whether any object has the key present with a nil
// (explicit null) value.
func hasExplicitNull(items []any, key string) bool {
	for _, it := range items {
		om, ok := it.(*orderedmap.OrderedMap)
		if !ok {
			continue
		}
		if v, present := om.Get(key); present && v == nil {
			return true
		}
	}
	return false
}

// buildRow builds one table row: for each ordered key, a Missing cell when the
// object lacks it, else cellFromValue of the present value. Non-object items
// (unreachable after the Compact gates) yield an empty row.
func buildRow(item any, orderedKeys []string, cfg CompactConfig) Row {
	om, ok := item.(*orderedmap.OrderedMap)
	if !ok {
		return NewRow(nil)
	}
	cells := make([]CellValue, len(orderedKeys))
	for i, k := range orderedKeys {
		v, present := om.Get(k)
		if !present {
			cells[i] = CellMissing{}
			continue
		}
		cells[i] = cellFromValue(v, cfg)
	}
	return NewRow(cells)
}

// cellFromValue maps a single value to a CellValue via the classifier: scalars and
// objects stay Scalar (flatten may promote objects later); an array of >=2 objects
// recurses into a Nested compaction; a bulky opaque STRING becomes an OpaqueRef
// (12-hex hashOpaque, byte_size in BYTES) with NO store side-effect. A non-string
// Opaque falls back to Scalar.
func cellFromValue(v any, cfg CompactConfig) CellValue {
	class := classifyCell(v, cfg.Classify)
	switch class.Kind {
	case ClassScalar:
		return CellScalar{Value: v}
	case ClassJsonObject:
		// Objects stay Scalar; flatten_uniform_nested may promote them later.
		return CellScalar{Value: v}
	case ClassJsonArray:
		if arr, ok := v.([]any); ok && len(arr) >= nestedRecurseMinLen && allObjects(arr) {
			inner := Compact(arr, cfg)
			return CellNested{Inner: &inner}
		}
		return CellScalar{Value: v}
	case ClassStringifiedJson:
		// DEFERRED (Plan 4+): the classifier never returns ClassStringifiedJson on
		// the compaction path in the MVP (deep stringified-JSON nesting is deferred).
		// Handle it defensively for parity with upstream: recurse if the parsed
		// value is an array of >=2 objects, else keep the parsed value as a Scalar.
		if arr, ok := class.Parsed.([]any); ok && len(arr) >= nestedRecurseMinLen && allObjects(arr) {
			inner := Compact(arr, cfg)
			return CellNested{Inner: &inner}
		}
		return CellScalar{Value: class.Parsed}
	case ClassOpaque:
		if s, ok := v.(string); ok {
			b := []byte(s)
			return CellOpaqueRef{
				CcrHash:  hashOpaque(b),
				ByteSize: len(b), // BYTE length.
				Kind:     class.OpaqueKind,
			}
		}
		// Non-string opaque is impossible from the classifier, but stay defensive.
		return CellScalar{Value: v}
	default:
		return CellScalar{Value: v}
	}
}

// allObjects reports whether every element of arr is a JSON object.
func allObjects(arr []any) bool {
	for _, e := range arr {
		if _, ok := e.(*orderedmap.OrderedMap); !ok {
			return false
		}
	}
	return true
}

// hashOpaque hashes payload bytes to a 12-char hex prefix: SHA-256, first 6 bytes,
// lowercase hex [ref: compactor.rs hash_opaque]. This is the compaction-stage /
// Tier-2 opaque-cell hash; it is NOT store-written and NOT round-trippable in the
// MVP (see the "Opaque-cell hash + store-write" contract). The store-writing,
// round-trippable opaque path is the walker's EmitOpaqueCCRMarker (24-hex BLAKE3).
func hashOpaque(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:6])
}

// flattenUniformNested performs the dotted-flatten pass in place. For each column
// (index i, walking upward as the schema grows) it checks whether every non-missing
// cell is an object with the EXACT SAME ORDERED inner key set (1..=MaxFlattenInnerKeys
// keys); if so it replaces the column with one dotted column per inner key
// (parent.name -> parent.name.k), promoting the inner values into the rows. Only one
// level is expanded per pass; already-dotted columns (name contains '.') are skipped.
func flattenUniformNested(specs *[]FieldSpec, rows []Row, cfg CompactConfig) {
	i := 0
	for i < len(*specs) {
		innerKeys := uniformObjectKeys(*specs, rows, i)
		if innerKeys == nil || len(innerKeys) == 0 || len(innerKeys) > cfg.MaxFlattenInnerKeys {
			i++
			continue
		}
		parentName := (*specs)[i].Name

		// Build the replacement column specs (placeholder type_tag "string",
		// nullable false — refined below).
		newSpecs := make([]FieldSpec, len(innerKeys))
		for j, k := range innerKeys {
			newSpecs[j] = FieldSpec{
				Name:     parentName + "." + k,
				TypeTag:  "string", // placeholder, refined via inferTypeTagFromCells.
				Nullable: false,
			}
		}

		// Splice specs: replace specs[i] with newSpecs.
		spliced := make([]FieldSpec, 0, len(*specs)-1+len(newSpecs))
		spliced = append(spliced, (*specs)[:i]...)
		spliced = append(spliced, newSpecs...)
		spliced = append(spliced, (*specs)[i+1:]...)
		*specs = spliced

		// Per row: remove cell i, insert the n_new expanded cells at i.
		for r := range rows {
			cell := rows[r].Cells[i]
			var innerObj *orderedmap.OrderedMap
			switch c := cell.(type) {
			case CellScalar:
				if om, ok := c.Value.(*orderedmap.OrderedMap); ok {
					innerObj = om
				} else {
					// Invariant from uniform_object_keys: a non-missing cell here is
					// always an object. Any other shape is unreachable.
					innerObj = nil
				}
			case CellMissing:
				innerObj = nil
			default:
				// Unreachable given uniformObjectKeys guarantees.
				innerObj = nil
			}

			expanded := make([]CellValue, len(innerKeys))
			for j, k := range innerKeys {
				if innerObj == nil {
					expanded[j] = CellMissing{}
					continue
				}
				if v, present := innerObj.Get(k); present {
					expanded[j] = cellFromValue(v, cfg)
				} else {
					expanded[j] = CellMissing{}
				}
			}

			newCells := make([]CellValue, 0, len(rows[r].Cells)-1+len(expanded))
			newCells = append(newCells, rows[r].Cells[:i]...)
			newCells = append(newCells, expanded...)
			newCells = append(newCells, rows[r].Cells[i+1:]...)
			rows[r].Cells = newCells
		}

		// Refine each new column's type tag + nullability from its actual cells.
		nNew := len(innerKeys)
		for j := 0; j < nNew; j++ {
			tag, nullable := inferTypeTagFromCells(rows, i+j)
			(*specs)[i+j].TypeTag = tag
			(*specs)[i+j].Nullable = nullable
		}

		// Advance past the newly expanded columns (one level per pass).
		i += nNew
	}
}

// uniformObjectKeys returns the shared ordered inner key set of column col when
// every non-missing cell in that column is an object with the EXACT SAME ORDERED
// keys (compared by ordered slice equality). It returns nil when the column is
// already dotted, when any cell is ragged/non-object, or when no object was seen.
func uniformObjectKeys(specs []FieldSpec, rows []Row, col int) []string {
	// Already-dotted columns are never re-flattened in the same pass.
	if strings.Contains(specs[col].Name, ".") {
		return nil
	}
	var canonical []string
	sawObject := false
	for r := range rows {
		if col >= len(rows[r].Cells) {
			return nil // ragged (defensive)
		}
		cell := rows[r].Cells[col]
		switch c := cell.(type) {
		case CellMissing:
			continue
		case CellScalar:
			om, ok := c.Value.(*orderedmap.OrderedMap)
			if !ok {
				return nil // non-object scalar -> not uniform
			}
			keys := om.Keys()
			sawObject = true
			if canonical == nil {
				canonical = append([]string(nil), keys...)
			} else if !equalStringSlices(canonical, keys) {
				return nil // ordered inequality -> not uniform
			}
		default:
			return nil // Nested/OpaqueRef -> not uniform
		}
	}
	if !sawObject {
		return nil
	}
	return canonical
}

// equalStringSlices reports ordered slice equality (order-sensitive).
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// inferTypeTagFromCells infers a column's type tag and nullability from its actual
// row cells: Missing/Scalar(Null) set nullable; the first non-null scalar sets the
// tag, a differing scalar type widens to "json"; Nested/OpaqueRef force "json".
func inferTypeTagFromCells(rows []Row, col int) (string, bool) {
	tag := "string"
	sawValue := false
	nullable := false
	for r := range rows {
		if col >= len(rows[r].Cells) {
			continue
		}
		switch c := rows[r].Cells[col].(type) {
		case CellMissing:
			nullable = true
		case CellScalar:
			if c.Value == nil {
				nullable = true
				continue
			}
			t := typeTagFor(c.Value)
			if !sawValue {
				tag = t
				sawValue = true
			} else if t != tag {
				tag = "json"
			}
		case CellNested, CellOpaqueRef:
			tag = "json"
		}
	}
	return tag, nullable
}

// inferTypeTag infers a column type tag by scanning every object that contains the
// key: nulls are skipped; the first non-null sets the tag; a differing type widens
// to "json" (and breaks). An all-null or absent column defaults to "string".
func inferTypeTag(items []any, key string) string {
	tag := ""
	set := false
	for _, it := range items {
		om, ok := it.(*orderedmap.OrderedMap)
		if !ok {
			continue
		}
		v, present := om.Get(key)
		if !present {
			continue
		}
		if v == nil {
			continue // Null skip.
		}
		t := typeTagFor(v)
		if !set {
			tag = t
			set = true
		} else if t != tag {
			return "json"
		}
	}
	if !set {
		return "string"
	}
	return tag
}

// typeTagFor maps a single decodeJSON value to its closed-set type tag. Integer
// vs float is decided by whether the json.Number token parses as an integer
// (mirrors serde Number::is_i64()||is_u64() — huge ints beyond 64-bit -> float).
func typeTagFor(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case *orderedmap.OrderedMap, []any:
		return "json"
	case string:
		return "string"
	case json.Number:
		if isIntegerNumber(val) {
			return "int"
		}
		return "float"
	default:
		// Non-json.Number numeric shapes should not appear on the UseNumber path,
		// but treat any other numeric as float defensively.
		return "float"
	}
}

// isIntegerNumber reports whether a json.Number token is an integer that fits in
// an int64 or uint64 (mirrors serde Number::is_i64() || is_u64()). A token like
// "1.0" or an integer beyond 64-bit range parses false -> float.
func isIntegerNumber(n json.Number) bool {
	s := n.String()
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseUint(s, 10, 64); err == nil {
		return true
	}
	return false
}
