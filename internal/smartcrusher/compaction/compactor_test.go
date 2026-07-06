package compaction

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// decodeArr decodes a JSON array source into the []any-of-*orderedmap.OrderedMap
// shape the compactor consumes. It reuses the package-local ordered decoder (see
// classifier_test.go's decode helper) to avoid an import cycle back to the parent
// smartcrusher package.
func decodeArr(t *testing.T, s string) []any {
	t.Helper()
	v := decode(t, s)
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("decodeArr(%q): got %T, want []any", s, v)
	}
	return arr
}

// scalarValue asserts the cell is a CellScalar and returns its Value.
func scalarValue(t *testing.T, c CellValue) any {
	t.Helper()
	sc, ok := c.(CellScalar)
	if !ok {
		t.Fatalf("cell = %T, want CellScalar", c)
	}
	return sc.Value
}

func TestCompactTooFewOrNonObjectUntouched(t *testing.T) {
	cfg := DefaultCompactConfig()

	cases := []struct {
		name  string
		input string
	}{
		{"empty", `[]`},
		{"single object (len<2)", `[{"a":1}]`},
		{"len<2 scalar", `[1]`},
		{"two but one non-object", `[{"a":1},2]`},
		{"two but one null", `[{"a":1},null]`},
		{"all scalars", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compact(decodeArr(t, tc.input), cfg)
			if got.WasCompacted() {
				t.Fatalf("Compact(%s) WasCompacted=true, want Untouched", tc.input)
			}
			u, ok := got.(Untouched)
			if !ok {
				t.Fatalf("Compact(%s) = %T, want Untouched", tc.input, got)
			}
			if _, ok := u.Value.([]any); !ok {
				t.Errorf("Untouched.Value = %T, want []any (the original array)", u.Value)
			}
		})
	}
}

func TestCompactHomogeneousTableBasic(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Three uniform objects -> Table with 3 columns, 3 rows, original_count=3.
	got := Compact(decodeArr(t, `[{"id":1,"level":"info","msg":"a"},{"id":2,"level":"info","msg":"b"},{"id":3,"level":"warn","msg":"c"}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact(uniform) = %T, want Table", got)
	}
	if tbl.OriginalCount != 3 {
		t.Errorf("OriginalCount = %d, want 3", tbl.OriginalCount)
	}
	if tbl.KeptRowCount() != 3 {
		t.Errorf("KeptRowCount = %d, want 3", tbl.KeptRowCount())
	}
	// All three keys are core (freq==total), so all present; ordering DESC freq
	// then ASC name. Here every key has freq 3, so ties break ASC name:
	// id, level, msg.
	wantNames := []string{"id", "level", "msg"}
	if got := tbl.Schema.FieldNames(); !equalStrings(got, wantNames) {
		t.Errorf("FieldNames = %v, want %v", got, wantNames)
	}
	// No sparse column, no explicit null -> nullable=false everywhere.
	for _, f := range tbl.Schema.Fields {
		if f.Nullable {
			t.Errorf("field %q nullable=true, want false (dense, no null)", f.Name)
		}
	}
	// type_tag: id int, level/msg string.
	wantTags := map[string]string{"id": "int", "level": "string", "msg": "string"}
	for _, f := range tbl.Schema.Fields {
		if f.TypeTag != wantTags[f.Name] {
			t.Errorf("field %q TypeTag = %q, want %q", f.Name, f.TypeTag, wantTags[f.Name])
		}
	}
}

func TestCompactFieldOrderingDescFreqThenAscName(t *testing.T) {
	cfg := DefaultCompactConfig()
	// "a" present in all 3 (freq 3, core); "z" present in 2 (freq 2); "b" present
	// in 2 (freq 2). With CoreFieldFraction 0.8, total=3 -> ceil(2.4)=3, so only
	// "a" is core, but ordered_keys includes ALL keys sorted DESC freq then ASC
	// name: a(3), then b(2) and z(2) tie -> ASC name b before z.
	got := Compact(decodeArr(t, `[{"a":1,"b":2,"z":3},{"a":4,"b":5},{"a":6,"z":7}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	wantNames := []string{"a", "b", "z"}
	if names := tbl.Schema.FieldNames(); !equalStrings(names, wantNames) {
		t.Errorf("FieldNames = %v, want %v (DESC freq, ties ASC name)", names, wantNames)
	}
	// "a" dense (freq 3 == total) -> not nullable; "b"/"z" sparse (freq 2 < 3) ->
	// nullable.
	for _, f := range tbl.Schema.Fields {
		wantNullable := f.Name != "a"
		if f.Nullable != wantNullable {
			t.Errorf("field %q nullable=%v, want %v", f.Name, f.Nullable, wantNullable)
		}
	}
}

func TestCompactNullableFromExplicitNull(t *testing.T) {
	cfg := DefaultCompactConfig()
	// "a" present in all rows (freq==total) but one row has explicit null ->
	// nullable=true even though dense.
	got := Compact(decodeArr(t, `[{"a":1,"b":2},{"a":null,"b":3},{"a":5,"b":6}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	var aField *FieldSpec
	for i := range tbl.Schema.Fields {
		if tbl.Schema.Fields[i].Name == "a" {
			aField = &tbl.Schema.Fields[i]
		}
	}
	if aField == nil {
		t.Fatalf("no field 'a'")
	}
	if !aField.Nullable {
		t.Errorf("field 'a' nullable=false, want true (dense but has explicit null)")
	}
}

func TestCompactCoreThresholdCeil(t *testing.T) {
	// core_threshold = ceil(total * CoreFieldFraction). total=5, 0.8 -> 4.0 exactly.
	// A key present in exactly 4 of 5 rows is core (freq 4 >= 4); a key in 3 is not.
	// This drives core_ratio, but since discriminator is DEFERRED it always falls
	// through to homogeneous; we assert the table still builds with all keys and
	// that the CEIL boundary is respected by nullable (freq<total => nullable).
	cfg := DefaultCompactConfig()
	// "a" in all 5 (freq 5, core, dense); "b" in 4 (freq 4, core, sparse);
	// "c" in 3 (freq 3, non-core, sparse).
	got := Compact(decodeArr(t, `[{"a":1,"b":2,"c":3},{"a":1,"b":2,"c":3},{"a":1,"b":2,"c":3},{"a":1,"b":2},{"a":1}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	if names := tbl.Schema.FieldNames(); !equalStrings(names, []string{"a", "b", "c"}) {
		t.Errorf("FieldNames = %v, want [a b c]", names)
	}
	wantNullable := map[string]bool{"a": false, "b": true, "c": true}
	for _, f := range tbl.Schema.Fields {
		if f.Nullable != wantNullable[f.Name] {
			t.Errorf("field %q nullable=%v, want %v", f.Name, f.Nullable, wantNullable[f.Name])
		}
	}
}

func TestCompactMissingCellDistinctFromNull(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Row 2 lacks "b"; row-cell must be CellMissing (structural), NOT
	// CellScalar{nil}.
	got := Compact(decodeArr(t, `[{"a":1,"b":2},{"a":3},{"a":4,"b":5}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	// column index of "b"
	bIdx := indexOf(tbl.Schema.FieldNames(), "b")
	if bIdx < 0 {
		t.Fatalf("no column 'b'")
	}
	// row 1 (second row) lacks b -> Missing.
	if _, ok := tbl.Rows[1].Cells[bIdx].(CellMissing); !ok {
		t.Errorf("row1 cell b = %T, want CellMissing", tbl.Rows[1].Cells[bIdx])
	}
	// row 0 has b=2 -> Scalar.
	if _, ok := tbl.Rows[0].Cells[bIdx].(CellScalar); !ok {
		t.Errorf("row0 cell b = %T, want CellScalar", tbl.Rows[0].Cells[bIdx])
	}
}

func TestCompactTypeTagIntVsFloat(t *testing.T) {
	cfg := DefaultCompactConfig()
	// "i" is always an integer token -> int; "f" always 1.0-style float -> float;
	// "h" is a huge integer beyond i64/u64 -> float.
	got := Compact(decodeArr(t, `[{"i":1,"f":1.0,"h":99999999999999999999999999},{"i":2,"f":2.5,"h":88888888888888888888888888}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	wantTags := map[string]string{"i": "int", "f": "float", "h": "float"}
	for _, f := range tbl.Schema.Fields {
		if f.TypeTag != wantTags[f.Name] {
			t.Errorf("field %q TypeTag = %q, want %q", f.Name, f.TypeTag, wantTags[f.Name])
		}
	}
}

func TestCompactTypeTagMixedJson(t *testing.T) {
	cfg := DefaultCompactConfig()
	// A field that is int in one row and string in another -> "json".
	got := Compact(decodeArr(t, `[{"a":1,"x":1},{"a":2,"x":"str"}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	for _, f := range tbl.Schema.Fields {
		if f.Name == "x" && f.TypeTag != "json" {
			t.Errorf("field 'x' TypeTag = %q, want json (mixed int/string)", f.TypeTag)
		}
	}
}

func TestCompactDottedFlattenUniform(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Every row's "meta" is an object with the EXACT SAME ORDERED key set
	// {region, zone}, inner count 2 (within 1..=6) -> flatten to meta.region,
	// meta.zone.
	got := Compact(decodeArr(t, `[{"id":1,"meta":{"region":"us","zone":"a"}},{"id":2,"meta":{"region":"eu","zone":"b"}}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	names := tbl.Schema.FieldNames()
	if indexOf(names, "meta") >= 0 {
		t.Errorf("column 'meta' still present after flatten; names=%v", names)
	}
	if indexOf(names, "meta.region") < 0 || indexOf(names, "meta.zone") < 0 {
		t.Errorf("flattened columns missing; names=%v want meta.region + meta.zone", names)
	}
	// Values should be the promoted inner scalars.
	rIdx := indexOf(names, "meta.region")
	if v := scalarValue(t, tbl.Rows[0].Cells[rIdx]); toStr(v) != "us" {
		t.Errorf("row0 meta.region = %v, want us", v)
	}
	// type_tag refined to string for meta.region after flatten.
	for _, f := range tbl.Schema.Fields {
		if f.Name == "meta.region" && f.TypeTag != "string" {
			t.Errorf("meta.region TypeTag = %q, want string", f.TypeTag)
		}
	}
}

func TestCompactFlattenInnerOpaqueStringStaysScalar(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Uniform-nested "meta" whose inner "blob" is a BULKY opaque string (>256 bytes,
	// low diversity, base64/html fail). The flatten pass must promote the inner value
	// as a PLAIN Scalar clone with NO re-classification [ref: flatten_uniform_nested
	// "else Scalar(clone map[k])"]. Re-classifying (the old cellFromValue bug) would
	// emit a CellOpaqueRef and tag the flattened column "json" instead of "string".
	blob := repeat("ab ", 100) // 300 bytes -> LongString opaque under cellFromValue.
	src := `[{"id":1,"meta":{"blob":"` + blob + `"}},{"id":2,"meta":{"blob":"` + blob + `"}}]`
	got := Compact(decodeArr(t, src), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	names := tbl.Schema.FieldNames()
	bIdx := indexOf(names, "meta.blob")
	if bIdx < 0 {
		t.Fatalf("flattened column 'meta.blob' missing; names=%v", names)
	}
	// The promoted cell must be a plain Scalar holding the RAW string, NOT an OpaqueRef.
	if _, isRef := tbl.Rows[0].Cells[bIdx].(CellOpaqueRef); isRef {
		t.Fatalf("row0 meta.blob = CellOpaqueRef, want CellScalar (no re-classification on flatten)")
	}
	if v := scalarValue(t, tbl.Rows[0].Cells[bIdx]); toStr(v) != blob {
		t.Errorf("row0 meta.blob scalar = %q..., want the raw blob", toStr(v))
	}
	// type_tag must be the scalar tag "string", NOT the Nested/OpaqueRef arm's "json".
	for _, f := range tbl.Schema.Fields {
		if f.Name == "meta.blob" && f.TypeTag != "string" {
			t.Errorf("meta.blob TypeTag = %q, want string (scalar tag, not json)", f.TypeTag)
		}
	}
}

func TestCompactFlattenInnerArrayOfObjectsStaysScalar(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Uniform-nested "meta" whose inner "items" is an array of >=2 objects. The
	// flatten pass must promote it as a PLAIN Scalar clone [ref: flatten_uniform_nested
	// "else Scalar(clone map[k])"]. Re-classifying (the old cellFromValue bug) would
	// recurse into a CellNested compaction instead of keeping the raw array scalar.
	src := `[{"id":1,"meta":{"items":[{"k":1},{"k":2}]}},{"id":2,"meta":{"items":[{"k":3},{"k":4}]}}]`
	got := Compact(decodeArr(t, src), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	names := tbl.Schema.FieldNames()
	iIdx := indexOf(names, "meta.items")
	if iIdx < 0 {
		t.Fatalf("flattened column 'meta.items' missing; names=%v", names)
	}
	// The promoted cell must be a plain Scalar (raw array), NOT a recursed CellNested.
	if _, isNested := tbl.Rows[0].Cells[iIdx].(CellNested); isNested {
		t.Fatalf("row0 meta.items = CellNested, want CellScalar (no re-classification on flatten)")
	}
	if _, ok := tbl.Rows[0].Cells[iIdx].(CellScalar); !ok {
		t.Fatalf("row0 meta.items = %T, want CellScalar", tbl.Rows[0].Cells[iIdx])
	}
}

func TestCompactFlattenDifferentOrderStaysNested(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Same key SET but different insertion ORDER -> NON-uniform (ordered slice
	// equality) -> stays nested as a Scalar cell (no flatten).
	got := Compact(decodeArr(t, `[{"id":1,"meta":{"region":"us","zone":"a"}},{"id":2,"meta":{"zone":"b","region":"eu"}}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	names := tbl.Schema.FieldNames()
	if indexOf(names, "meta") < 0 {
		t.Errorf("column 'meta' flattened despite differing key order; names=%v", names)
	}
	if indexOf(names, "meta.region") >= 0 {
		t.Errorf("meta.region present; flatten should NOT have fired for differing order")
	}
}

func TestCompactFlattenTooManyInnerKeysStaysNested(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Inner object has 7 keys > MaxFlattenInnerKeys(6) -> no flatten.
	got := Compact(decodeArr(t, `[{"id":1,"m":{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7}},{"id":2,"m":{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7}}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	if indexOf(tbl.Schema.FieldNames(), "m") < 0 {
		t.Errorf("column 'm' flattened despite 7 inner keys > 6; names=%v", tbl.Schema.FieldNames())
	}
}

func TestCompactAlreadyDottedColumnSkipped(t *testing.T) {
	cfg := DefaultCompactConfig()
	// A literal dotted key "a.b" whose value is an object must NOT be flattened
	// (dotted columns are skipped in the same pass).
	got := Compact(decodeArr(t, `[{"a.b":{"x":1}},{"a.b":{"x":2}}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	names := tbl.Schema.FieldNames()
	if indexOf(names, "a.b") < 0 {
		t.Errorf("dotted column 'a.b' was flattened/renamed; names=%v", names)
	}
	if indexOf(names, "a.b.x") >= 0 {
		t.Errorf("a.b.x present; a dotted column should be skipped for flatten")
	}
}

func TestCompactNestedArrayOfObjects(t *testing.T) {
	cfg := DefaultCompactConfig()
	// A column whose value is an array of >=2 objects -> Nested(compact(inner)).
	got := Compact(decodeArr(t, `[{"id":1,"items":[{"k":1},{"k":2}]},{"id":2,"items":[{"k":3},{"k":4}]}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	iIdx := indexOf(tbl.Schema.FieldNames(), "items")
	if iIdx < 0 {
		t.Fatalf("no column 'items'")
	}
	nested, ok := tbl.Rows[0].Cells[iIdx].(CellNested)
	if !ok {
		t.Fatalf("row0 items cell = %T, want CellNested", tbl.Rows[0].Cells[iIdx])
	}
	if nested.Inner == nil {
		t.Fatalf("CellNested.Inner is nil")
	}
	if _, ok := (*nested.Inner).(Table); !ok {
		t.Errorf("nested inner = %T, want Table", *nested.Inner)
	}
}

func TestCompactArrayLenBelow2StaysScalar(t *testing.T) {
	cfg := DefaultCompactConfig()
	// An array-valued column with only 1 object (< recurse-min 2) stays Scalar.
	got := Compact(decodeArr(t, `[{"id":1,"items":[{"k":1}]},{"id":2,"items":[{"k":2}]}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	iIdx := indexOf(tbl.Schema.FieldNames(), "items")
	if _, ok := tbl.Rows[0].Cells[iIdx].(CellScalar); !ok {
		t.Errorf("row0 items cell = %T, want CellScalar (array len<2)", tbl.Rows[0].Cells[iIdx])
	}
}

func TestCompactOpaqueCellFromString(t *testing.T) {
	cfg := DefaultCompactConfig()
	// A bulky string cell (>256 low-diversity, base64/html fail) -> OpaqueRef with
	// 12-hex hashOpaque and byte_size = BYTE length. No store side-effect exists
	// (the compactor has no store handle).
	blob := repeat("ab ", 100) // 300 bytes -> LongString opaque
	got := Compact(decodeArr(t, `[{"id":1,"blob":"`+blob+`"},{"id":2,"blob":"`+blob+`"}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	bIdx := indexOf(tbl.Schema.FieldNames(), "blob")
	ref, ok := tbl.Rows[0].Cells[bIdx].(CellOpaqueRef)
	if !ok {
		t.Fatalf("row0 blob cell = %T, want CellOpaqueRef", tbl.Rows[0].Cells[bIdx])
	}
	if len(ref.CcrHash) != 12 {
		t.Errorf("CcrHash len = %d (%q), want 12-hex", len(ref.CcrHash), ref.CcrHash)
	}
	if ref.ByteSize != len(blob) {
		t.Errorf("ByteSize = %d, want %d (BYTE length)", ref.ByteSize, len(blob))
	}
	// Hash must equal SHA-256 first 6 bytes hex of the blob bytes.
	sum := sha256.Sum256([]byte(blob))
	wantHash := hex.EncodeToString(sum[:6])
	if ref.CcrHash != wantHash {
		t.Errorf("CcrHash = %q, want %q (SHA-256[:6] hex)", ref.CcrHash, wantHash)
	}
	if ref.Kind.String() != LongString.String() {
		t.Errorf("Kind = %q, want %q", ref.Kind.String(), LongString.String())
	}
}

func TestCompactNonStringOpaqueStaysScalar(t *testing.T) {
	cfg := DefaultCompactConfig()
	// Opaque classification only produces OpaqueRef for String values; a non-string
	// (object) never becomes OpaqueRef here. A plain object cell stays Scalar
	// (flatten may promote later, but with differing/non-uniform shape it stays
	// Scalar). Use non-uniform inner objects to prevent flatten.
	got := Compact(decodeArr(t, `[{"id":1,"m":{"a":1}},{"id":2,"m":{"b":2}}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact = %T, want Table", got)
	}
	mIdx := indexOf(tbl.Schema.FieldNames(), "m")
	if _, ok := tbl.Rows[0].Cells[mIdx].(CellScalar); !ok {
		t.Errorf("row0 m cell = %T, want CellScalar (object, not OpaqueRef)", tbl.Rows[0].Cells[mIdx])
	}
}

func TestCompactHashOpaqueVector(t *testing.T) {
	// Direct hashOpaque reference vector: SHA-256 first 6 bytes hex.
	in := []byte("hello world")
	sum := sha256.Sum256(in)
	want := hex.EncodeToString(sum[:6])
	if got := hashOpaque(in); got != want {
		t.Errorf("hashOpaque(%q) = %q, want %q", in, got, want)
	}
	if len(hashOpaque(in)) != 12 {
		t.Errorf("hashOpaque hex width = %d, want 12", len(hashOpaque(in)))
	}
}

func TestCompactAllEmptyObjectsZeroFields(t *testing.T) {
	cfg := DefaultCompactConfig()
	// All-empty objects: total_keys==0 -> core_ratio hardcoded 1.0 -> homogeneous
	// table with zero fields, N rows.
	got := Compact(decodeArr(t, `[{},{}]`), cfg)
	tbl, ok := got.(Table)
	if !ok {
		t.Fatalf("Compact([{},{}]) = %T, want Table", got)
	}
	if len(tbl.Schema.Fields) != 0 {
		t.Errorf("Schema.Fields len = %d, want 0 (all-empty objects)", len(tbl.Schema.Fields))
	}
	if tbl.KeptRowCount() != 2 {
		t.Errorf("KeptRowCount = %d, want 2", tbl.KeptRowCount())
	}
}

// --- small local helpers (test-only) ---

func equalStrings(a, b []string) bool {
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

func indexOf(ss []string, target string) int {
	for i, s := range ss {
		if s == target {
			return i
		}
	}
	return -1
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
