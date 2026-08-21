// Package compaction implements the lossless compaction-table stage of the
// SmartCrusher: it turns a homogeneous JSON array-of-objects into a compact
// CSV-schema (or JSON/markdown-kv) rendering, replacing bulky opaque leaves with
// CCR cell markers. It is a clean-room Go port of upstream chopratejas/headroom's
// crates/headroom-core/src/transforms/smart_crusher/compaction/*.
//
// This sub-package is a leaf-to-stage chain: ir.go holds the renderer-agnostic IR
// (Schema/Row/Cell/Compaction), classifier.go decides which cells become opaque
// CCR cells, and the later compactor/formatter/walker/stage files build and render
// the IR. All JSON objects on the path are decoded with
// github.com/iancoleman/orderedmap; map[string]any is BANNED because key
// insertion/parse order is load-bearing (header + per-row cell order).
//
// To avoid an import cycle back to the parent package, compaction does NOT import
// smartcrusher; production code parses JSON via a decoder local to this package,
// and tests use a small local decode helper.
//
// Decision B — NO byte-parity. Float formatting and Python cosmetics are NOT
// replicated; only behavior, ratio bounds, and marker/strategy SHAPES are
// captured. Byte lengths use len(string) (Rust .len() is a BYTE count).
package compaction
