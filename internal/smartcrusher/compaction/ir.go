package compaction

// This file holds the renderer-agnostic IR that sits between the compactor
// (producer) and the formatter (consumer): types only, no algorithms, constants,
// or I/O [ref: compaction/ir.rs]. Go has no sum types, so the two Rust enums
// become sealed interfaces (CellValue, Compaction) whose only implementations are
// declared here.

// OpaqueKind labels an opaque (CCR-substituted) blob. It is string-backed so the
// tag flows directly into the comma-form marker KIND field downstream. Base64Blob,
// LongString, and HtmlChunk carry byte-pinned tags; Other(name) carries a raw name
// and is routed downstream AS LongString.
type OpaqueKind struct {
	// tag is the marker KIND string. For the three named kinds it is a fixed
	// literal; for Other it is the caller-supplied name.
	tag string
}

var (
	// Base64Blob/LongString/HtmlChunk carry byte-pinned KIND tags (do NOT rename).
	Base64Blob = OpaqueKind{tag: "base64"}
	LongString = OpaqueKind{tag: "string"}
	HtmlChunk  = OpaqueKind{tag: "html"}
)

// Other builds a routing-deferred OpaqueKind whose KIND tag is the given name.
// Downstream it is treated as LongString.
func Other(name string) OpaqueKind { return OpaqueKind{tag: name} }

// String returns the KIND tag used in the comma-form CCR marker.
func (k OpaqueKind) String() string { return k.tag }

// FieldSpec is one column of a compaction Schema. Name may be dotted
// (e.g. "meta.region" after flatten); TypeTag is drawn from the closed set
// {int,float,string,bool,null,json,ccr}; Nullable is true iff some row lacks the
// field or has an explicit null.
type FieldSpec struct {
	Name     string
	TypeTag  string
	Nullable bool
}

// Schema is the ordered column set of a Table. Field order is load-bearing (it
// drives both the header and per-row cell order).
type Schema struct {
	Fields []FieldSpec
}

// FieldNames returns the column names in declared order.
func (s Schema) FieldNames() []string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	return names
}

// CellValue is the sealed IR cell type. Its only implementations are CellScalar,
// CellNested, CellOpaqueRef, and CellMissing. CellMissing is DISTINCT from
// CellScalar{Value:nil} and must never be normalized to it.
type CellValue interface{ isCellValue() }

// CellScalar holds a plain scalar (or object-valued) cell. Value is in the
// decodeJSON shape (json.Number, string, bool, nil, or *orderedmap.OrderedMap /
// []any for object/array-valued scalars).
type CellScalar struct{ Value any }

// CellNested holds a recursively compacted sub-array (the recursion hook). The
// inner compaction is a pointer to replace the Rust Box.
type CellNested struct{ Inner *Compaction }

// CellOpaqueRef is a CCR pointer cell: a hash, the payload's byte size, and its
// opaque kind. It is rendered later via ccr.MarkerForCell.
type CellOpaqueRef struct {
	CcrHash  string
	ByteSize int
	Kind     OpaqueKind
}

// CellMissing marks a row that lacks the column entirely (structurally distinct
// from an explicit null scalar).
type CellMissing struct{}

func (CellScalar) isCellValue()    {}
func (CellNested) isCellValue()    {}
func (CellOpaqueRef) isCellValue() {}
func (CellMissing) isCellValue()   {}

// Row is one table row. Cell order and length match the parent Schema.Fields
// (cells[i] binds to fields[i] by position only).
type Row struct {
	Cells []CellValue
}

// NewRow constructs a Row from the given cells.
func NewRow(cells []CellValue) Row { return Row{Cells: cells} }

// Len returns the number of cells.
func (r Row) Len() int { return len(r.Cells) }

// IsEmpty reports whether the row has no cells.
func (r Row) IsEmpty() bool { return len(r.Cells) == 0 }

// Bucket is one discriminator bucket of a Buckets compaction.
//
// DEFERRED (Plan 4+): the bucket/discriminator path is defined for tree
// completeness but never produced in the MVP.
type Bucket struct {
	// Key is the discriminator value (decodeJSON shape).
	Key    any
	Schema Schema
	Rows   []Row
}

// Compaction is the sealed IR result of compacting an array: Table, Buckets,
// OpaqueRef, or Untouched. Untouched is the ONLY non-compacted variant.
type Compaction interface {
	isCompaction() bool
	// KeptRowCount is the number of rows retained (0 for OpaqueRef/Untouched).
	KeptRowCount() int
	// OriginalRowCount is the array's original length (0 for OpaqueRef/Untouched).
	OriginalRowCount() int
	// WasCompacted is true for Table/Buckets/OpaqueRef; false ONLY for Untouched.
	WasCompacted() bool
}

// Table is a homogeneous compaction table. OriginalCount is captured BEFORE any
// drop; the drop count (original - kept) is not stored.
type Table struct {
	Schema        Schema
	Rows          []Row
	OriginalCount int
}

func (Table) isCompaction() bool      { return true }
func (t Table) KeptRowCount() int     { return len(t.Rows) }
func (t Table) OriginalRowCount() int { return t.OriginalCount }
func (Table) WasCompacted() bool      { return true }

// Buckets is the discriminator-bucketed compaction.
//
// DEFERRED (Plan 4+): defined for tree completeness; the compactor never produces
// this variant in the MVP (it always falls through to the homogeneous Table).
type Buckets struct {
	Discriminator string
	Buckets       []Bucket
	OriginalCount int
}

func (Buckets) isCompaction() bool { return true }
func (b Buckets) KeptRowCount() int {
	total := 0
	for _, bk := range b.Buckets {
		total += len(bk.Rows)
	}
	return total
}
func (b Buckets) OriginalRowCount() int { return b.OriginalCount }
func (Buckets) WasCompacted() bool      { return true }

// OpaqueRef is a whole-array-as-opaque-blob compaction (the array collapsed to a
// single CCR pointer).
type OpaqueRef struct {
	CcrHash  string
	ByteSize int
	Kind     OpaqueKind
}

func (OpaqueRef) isCompaction() bool    { return true }
func (OpaqueRef) KeptRowCount() int     { return 0 }
func (OpaqueRef) OriginalRowCount() int { return 0 }
func (OpaqueRef) WasCompacted() bool    { return true }

// Untouched wraps the original value when compaction declined; the caller routes
// it to the lossy fallback. It is the ONLY non-compacted Compaction variant.
type Untouched struct {
	Value any
}

func (Untouched) isCompaction() bool    { return true }
func (Untouched) KeptRowCount() int     { return 0 }
func (Untouched) OriginalRowCount() int { return 0 }
func (Untouched) WasCompacted() bool    { return false }
