package compaction

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/iancoleman/orderedmap"
)

// This file is the recursive document driver [ref: compaction/walker.rs]. The
// DocumentCompactor descends a decoded JSON document and replaces bulky leaves in
// place while preserving shape and key order. It owns NO compaction logic (it
// reuses Compact, a Formatter, and classifyCell). The load-bearing invariant is
// recurse-BEFORE-compact for arrays, so deep nesting cascades bottom-up. Dotted
// flatten is NOT here (it lives in the compactor).
//
// Opaque-blob path (round-trippable): EmitOpaqueCCRMarker hashes with
// ccr.ComputeKey (BLAKE3 -> 24-hex) and writes the store when it is non-nil, so a
// walked opaque blob round-trips via store.Get(hash). This is distinct from the
// compactor's Tier-2 cell path (12-hex hashOpaque, NOT store-written).

// DocumentCompactor walks a JSON document, replacing compactable spots. It is the
// store-carrying type (CompactionStage deliberately is not). CcrStore is nilable;
// when nil, opaque blobs are still marked but nothing is stashed.
type DocumentCompactor struct {
	Config    CompactConfig
	Formatter Formatter
	CcrStore  ccr.Store // nilable
}

// Compact walks doc and returns a same-shaped value with compactable spots
// replaced. Object key order and array element order are preserved.
func (d *DocumentCompactor) Compact(doc any) any {
	return d.walk(doc)
}

// walk dispatches on value shape: object -> walkObject; array -> walkArray;
// string -> walkString; any scalar is returned unchanged.
func (d *DocumentCompactor) walk(v any) any {
	switch val := v.(type) {
	case *orderedmap.OrderedMap:
		return d.walkObject(val)
	case []any:
		return d.walkArray(val)
	case string:
		return d.walkString(val)
	default:
		return v
	}
}

// walkObject builds a NEW object with the SAME key order, each value replaced by
// walk(value). An empty object stays empty.
func (d *DocumentCompactor) walkObject(om *orderedmap.OrderedMap) any {
	out := orderedmap.New()
	for _, k := range om.Keys() {
		v, _ := om.Get(k)
		out.Set(k, d.walk(v))
	}
	return out
}

// walkArray recurses into every element FIRST (into a new same-length slice), then
// compacts the recursed slice. When the array compacted, the whole array is
// replaced by a single formatter-rendered string; otherwise the recursed slice is
// returned.
func (d *DocumentCompactor) walkArray(items []any) any {
	inner := make([]any, len(items))
	for i, it := range items {
		inner[i] = d.walk(it)
	}
	c := Compact(inner, d.Config)
	if c.WasCompacted() {
		return d.Formatter.Format(&c)
	}
	return inner
}

// walkString handles a string leaf: (1) if it parses as a JSON container, walk the
// parsed value — if that produced a String (a rendered sub-table) return it, else
// re-serialize the recursed value compact (falling back to the original on
// serialize failure); (2) else classify the string, and if Opaque emit a
// store-writing comma-form CCR marker; (3) else return the string unchanged.
func (d *DocumentCompactor) walkString(s string) any {
	if parsed, ok := TryParseJSONContainer(s); ok {
		recursed := d.walk(parsed)
		if str, isStr := recursed.(string); isStr {
			return str
		}
		if out := compactJSON(recursed); out != "" {
			return out
		}
		return s // serialize failure -> keep the original.
	}
	class := classifyString(s, d.Config.Classify)
	if class.Kind == ClassOpaque {
		return EmitOpaqueCCRMarker(s, class.OpaqueKind, d.CcrStore)
	}
	return s
}

// CompactDocument is a convenience wrapper: default config + CSV-schema formatter +
// no CCR store.
func CompactDocument(doc any) any {
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}
	return d.Compact(doc)
}

// TryParseJSONContainer parses s ONLY when its first non-whitespace rune is '{' or
// '[' AND the whole ORIGINAL string parses to an object or array. Bare scalars
// (numbers, bools, quoted strings) never recurse. It reuses the package-local
// ordered decoder so key order is preserved.
func TryParseJSONContainer(s string) (any, bool) {
	r := firstNonSpaceRune(s)
	if r != '{' && r != '[' {
		return nil, false
	}
	return parseContainer(s)
}

// EmitOpaqueCCRMarker builds the comma-form marker <<ccr:HASH,KIND,SIZE>> for an
// opaque payload and, when store is non-nil, stashes the payload under HASH so it
// round-trips. The hash is ccr.ComputeKey (BLAKE3 -> 24-hex); the hash is computed
// identically whether or not a store is present. SIZE is the raw byte length via
// the shared comma-form builder (see formatCcrMarker's integrator note).
func EmitOpaqueCCRMarker(payload string, kind OpaqueKind, store ccr.Store) string {
	hash := ccr.ComputeKey([]byte(payload))
	if store != nil {
		store.Put(hash, payload)
	}
	return formatCcrMarker(hash, len(payload), kind)
}
