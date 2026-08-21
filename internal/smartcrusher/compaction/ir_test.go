package compaction

import "testing"

// TestSchemaFieldNamesOrder asserts FieldNames returns names in declared order
// (header + per-row cell order is load-bearing) [ref: ir.rs goPortNotes].
func TestSchemaFieldNamesOrder(t *testing.T) {
	s := Schema{Fields: []FieldSpec{
		{Name: "id", TypeTag: "int", Nullable: false},
		{Name: "meta.region", TypeTag: "string", Nullable: true},
		{Name: "score", TypeTag: "float", Nullable: false},
	}}
	got := s.FieldNames()
	want := []string{"id", "meta.region", "score"}
	if len(got) != len(want) {
		t.Fatalf("FieldNames() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FieldNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestUntouchedNotCompacted asserts the Untouched variant reports itself as the
// only non-compacted shape, with zero kept/original rows [ref: ir.rs edgeCases].
func TestUntouchedNotCompacted(t *testing.T) {
	c := Untouched{Value: "anything"}
	if c.WasCompacted() {
		t.Errorf("Untouched.WasCompacted() = true, want false")
	}
	if got := c.KeptRowCount(); got != 0 {
		t.Errorf("Untouched.KeptRowCount() = %d, want 0", got)
	}
	if got := c.OriginalRowCount(); got != 0 {
		t.Errorf("Untouched.OriginalRowCount() = %d, want 0", got)
	}
}

// TestTableRowCounts asserts a Table with 2 kept rows out of an original 5
// reports compacted/2/5 [ref: ir.rs goPortNotes Table{2,5}].
func TestTableRowCounts(t *testing.T) {
	tbl := Table{
		Schema:        Schema{Fields: []FieldSpec{{Name: "a", TypeTag: "int"}}},
		Rows:          []Row{NewRow([]CellValue{CellScalar{Value: nil}}), NewRow([]CellValue{CellScalar{Value: nil}})},
		OriginalCount: 5,
	}
	if !tbl.WasCompacted() {
		t.Errorf("Table.WasCompacted() = false, want true")
	}
	if got := tbl.KeptRowCount(); got != 2 {
		t.Errorf("Table.KeptRowCount() = %d, want 2", got)
	}
	if got := tbl.OriginalRowCount(); got != 5 {
		t.Errorf("Table.OriginalRowCount() = %d, want 5", got)
	}
}

// TestBucketsRowCounts asserts Buckets (DEFERRED but defined) reports the sum of
// bucket rows kept and the stored original [ref: ir.rs goPortNotes Buckets{2+1,10}].
func TestBucketsRowCounts(t *testing.T) {
	b := Buckets{
		Discriminator: "kind",
		Buckets: []Bucket{
			{Rows: []Row{{}, {}}},
			{Rows: []Row{{}}},
		},
		OriginalCount: 10,
	}
	if !b.WasCompacted() {
		t.Errorf("Buckets.WasCompacted() = false, want true")
	}
	if got := b.KeptRowCount(); got != 3 {
		t.Errorf("Buckets.KeptRowCount() = %d, want 3", got)
	}
	if got := b.OriginalRowCount(); got != 10 {
		t.Errorf("Buckets.OriginalRowCount() = %d, want 10", got)
	}
}

// TestOpaqueRefRowCounts asserts the OpaqueRef Compaction variant reports
// compacted but zero kept/original rows [ref: ir.rs edgeCases].
func TestOpaqueRefRowCounts(t *testing.T) {
	c := OpaqueRef{CcrHash: "abc123", ByteSize: 512, Kind: LongString}
	if !c.WasCompacted() {
		t.Errorf("OpaqueRef.WasCompacted() = false, want true")
	}
	if got := c.KeptRowCount(); got != 0 {
		t.Errorf("OpaqueRef.KeptRowCount() = %d, want 0", got)
	}
	if got := c.OriginalRowCount(); got != 0 {
		t.Errorf("OpaqueRef.OriginalRowCount() = %d, want 0", got)
	}
}

// TestCellMissingDistinctFromScalarNull asserts CellMissing is structurally
// distinct from CellScalar{Value:nil} — the two must never be normalized to each
// other [ref: ir.rs edgeCases cell_missing_distinct_from_scalar_null].
func TestCellMissingDistinctFromScalarNull(t *testing.T) {
	var missing CellValue = CellMissing{}
	var scalarNull CellValue = CellScalar{Value: nil}

	if _, ok := missing.(CellMissing); !ok {
		t.Errorf("CellMissing{} did not type-assert to CellMissing")
	}
	if _, ok := scalarNull.(CellMissing); ok {
		t.Errorf("CellScalar{Value:nil} incorrectly type-asserts to CellMissing")
	}
	if _, ok := scalarNull.(CellScalar); !ok {
		t.Errorf("CellScalar{Value:nil} did not type-assert to CellScalar")
	}
}

// TestRowHelpers asserts NewRow/Len/IsEmpty behave as the position-bound row
// contract requires [ref: ir.rs publicApi].
func TestRowHelpers(t *testing.T) {
	empty := NewRow(nil)
	if !empty.IsEmpty() {
		t.Errorf("NewRow(nil).IsEmpty() = false, want true")
	}
	if got := empty.Len(); got != 0 {
		t.Errorf("NewRow(nil).Len() = %d, want 0", got)
	}
	r := NewRow([]CellValue{CellScalar{Value: nil}, CellMissing{}})
	if r.IsEmpty() {
		t.Errorf("NewRow(2 cells).IsEmpty() = true, want false")
	}
	if got := r.Len(); got != 2 {
		t.Errorf("NewRow(2 cells).Len() = %d, want 2", got)
	}
}

// TestOpaqueKindTag asserts the string-backed OpaqueKind tags match the
// byte-pinned literals used downstream in the comma-form marker KIND field, and
// that Other routes as LongString's tag semantics [ref: ir.rs / walker.rs].
func TestOpaqueKindTag(t *testing.T) {
	cases := []struct {
		kind OpaqueKind
		want string
	}{
		{Base64Blob, "base64"},
		{LongString, "string"},
		{HtmlChunk, "html"},
		{Other("custom"), "custom"},
	}
	for _, c := range cases {
		if got := c.kind.String(); got != c.want {
			t.Errorf("OpaqueKind %#v .String() = %q, want %q", c.kind, got, c.want)
		}
	}
}
