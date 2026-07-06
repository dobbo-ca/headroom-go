package compaction

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/iancoleman/orderedmap"
)

// This file is the RENDERING layer: it walks a Compaction IR tree into bytes
// [ref: compaction/formatter.rs]. It does NOT contain the 0.30 savings gate (that
// lives upstream in the crusher's crushArray). Three impls exist: JsonFormatter,
// CsvSchemaFormatter (the headline MVP), and MarkdownKvFormatter (opt-in, body
// DEFERRED). All opaque cells render through ONE comma-form CCR marker builder
// (ccr.MarkerForCell). Decision B: no float-parity; byte lengths use len(string).

// Formatter renders a Compaction IR to a string. Name is a stable tag; Format
// produces the rendering; EstimateBytes MUST equal len(Format(c)) (a tested
// invariant — Go has no default methods, so each struct implements it as
// len(Format(c))).
type Formatter interface {
	Name() string
	Format(c *Compaction) string
	EstimateBytes(c *Compaction) int
}

// CsvSchemaFormatter renders a Table as a "[N]{col:type[?],...}" declaration line
// followed by one CSV row per KEPT row. It is the MVP default.
type CsvSchemaFormatter struct {
	// IncludeDropSummary appends " __dropped:K" to the declaration when kept rows
	// are fewer than the original count.
	//
	// DEFERRED (Plan 4+): low-priority cosmetic; wired but off by default.
	IncludeDropSummary bool
}

// Name returns the stable tag "csv-schema".
func (CsvSchemaFormatter) Name() string { return "csv-schema" }

// EstimateBytes returns len(Format(c)) (the tested invariant).
func (f CsvSchemaFormatter) EstimateBytes(c *Compaction) int { return len(f.Format(c)) }

// Format renders the Compaction. Table -> declaration + rows; top-level OpaqueRef
// -> comma-form marker; Untouched -> compact JSON; Buckets -> DEFERRED stub.
func (f CsvSchemaFormatter) Format(c *Compaction) string {
	switch v := (*c).(type) {
	case Table:
		return f.formatTable(v)
	case OpaqueRef:
		return formatCcrMarker(v.CcrHash, v.ByteSize, v.Kind)
	case Untouched:
		return compactJSON(v.Value)
	case Buckets:
		// DEFERRED (Plan 4+): the heterogeneous Buckets rendering is not produced in
		// the MVP; render nothing meaningful.
		return ""
	default:
		return ""
	}
}

// formatTable writes the declaration line and one CSV row per kept row.
func (f CsvSchemaFormatter) formatTable(t Table) string {
	var b strings.Builder
	// Declaration: '[' N ']{' col:type[?] ',' ... '}'.
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(len(t.Rows))) // N = KEPT rows.
	b.WriteString("]{")
	for i, fld := range t.Schema.Fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fld.Name)
		b.WriteByte(':')
		b.WriteString(fld.TypeTag)
		if fld.Nullable {
			b.WriteByte('?')
		}
	}
	b.WriteByte('}')
	if f.IncludeDropSummary && len(t.Rows) < t.OriginalCount {
		b.WriteString(" __dropped:")
		b.WriteString(strconv.Itoa(t.OriginalCount - len(t.Rows)))
	}
	b.WriteByte('\n')
	// Rows: each cell via formatCell, joined by ',', one line each.
	for _, row := range t.Rows {
		for i, cell := range row.Cells {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(csvFormatCell(cell))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// csvFormatCell renders one cell for the CSV body: Missing -> ""; Scalar ->
// jsonScalarToCsv; Nested -> csvQuote(JsonFormatter output); OpaqueRef ->
// comma-form marker.
func csvFormatCell(cell CellValue) string {
	switch c := cell.(type) {
	case CellMissing:
		return ""
	case CellScalar:
		return jsonScalarToCsv(c.Value)
	case CellNested:
		return csvQuote(JsonFormatter{}.Format(c.Inner))
	case CellOpaqueRef:
		return formatCcrMarker(c.CcrHash, c.ByteSize, c.Kind)
	default:
		return ""
	}
}

// jsonScalarToCsv renders a scalar value for a CSV cell. Null -> "" (empty cell,
// UNLIKE the KV "null" literal); bool -> true/false; number -> its literal form;
// string -> raw unless it needs quoting; object/array -> quoted compact JSON.
func jsonScalarToCsv(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(val)
	case json.Number:
		return val.String()
	case string:
		if needsCsvQuote(val) {
			return csvQuote(val)
		}
		return val
	case *orderedmap.OrderedMap, []any:
		return csvQuote(compactJSON(val))
	default:
		return csvQuote(compactJSON(val))
	}
}

// needsCsvQuote reports whether s must be quoted: it contains a comma, a double
// quote, a newline, or a carriage return.
func needsCsvQuote(s string) bool {
	return strings.ContainsAny(s, ",\"\n\r")
}

// csvQuote wraps s in double quotes and doubles internal double-quotes (RFC-4180);
// newlines and commas are literal inside the quotes.
func csvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// JsonFormatter renders a Compaction as a JSON wrapper. It is needed by the CSV
// and KV formatters for nested-cell rendering. Pretty toggles indentation.
type JsonFormatter struct {
	Pretty bool
}

// Name returns the stable tag "json".
func (JsonFormatter) Name() string { return "json" }

// EstimateBytes returns len(Format(c)) (the tested invariant).
func (f JsonFormatter) EstimateBytes(c *Compaction) int { return len(f.Format(c)) }

// Format renders the Compaction as JSON (single line unless Pretty).
func (f JsonFormatter) Format(c *Compaction) string {
	v := compactionToJSON(*c)
	if f.Pretty {
		return marshalOrdered(v, true)
	}
	return marshalOrdered(v, false)
}

// compactionToJSON converts a Compaction IR to its JSON wrapper value.
func compactionToJSON(c Compaction) any {
	switch v := c.(type) {
	case Table:
		om := orderedmap.New()
		om.Set("_compaction", "table")
		om.Set("_schema", schemaToJSON(v.Schema))
		om.Set("_kept", len(v.Rows))
		om.Set("_total", v.OriginalCount)
		rows := make([]any, len(v.Rows))
		for i, row := range v.Rows {
			cells := make([]any, len(row.Cells))
			for j, cell := range row.Cells {
				cells[j] = cellToJSON(cell)
			}
			rows[i] = cells
		}
		om.Set("_rows", rows)
		return om
	case OpaqueRef:
		// Node-level OpaqueRef: {_compaction:"ccr",_hash,_size,_kind} — DIFFERENT
		// from the cell-level {_ccr,_size,_kind} shape.
		om := orderedmap.New()
		om.Set("_compaction", "ccr")
		om.Set("_hash", v.CcrHash)
		om.Set("_size", v.ByteSize)
		om.Set("_kind", v.Kind.String())
		return om
	case Untouched:
		return v.Value
	case Buckets:
		// DEFERRED (Plan 4+): heterogeneous Buckets JSON not produced in the MVP.
		return nil
	default:
		return nil
	}
}

// schemaToJSON renders a Schema as an array of {name, type[, nullable:true]}.
func schemaToJSON(s Schema) any {
	fields := make([]any, len(s.Fields))
	for i, f := range s.Fields {
		om := orderedmap.New()
		om.Set("name", f.Name)
		om.Set("type", f.TypeTag) // key "type" (NOT type_tag).
		if f.Nullable {
			om.Set("nullable", true) // only present when true.
		}
		fields[i] = om
	}
	return fields
}

// cellToJSON renders one cell: Scalar verbatim; Missing -> null; Nested ->
// compactionToJSON; OpaqueRef CELL -> {_ccr,_size,_kind}.
func cellToJSON(cell CellValue) any {
	switch c := cell.(type) {
	case CellScalar:
		return c.Value
	case CellMissing:
		return nil
	case CellNested:
		return compactionToJSON(*c.Inner)
	case CellOpaqueRef:
		om := orderedmap.New()
		om.Set("_ccr", c.CcrHash)
		om.Set("_size", c.ByteSize)
		om.Set("_kind", c.Kind.String())
		return om
	default:
		return nil
	}
}

// MarkdownKvFormatter is the opt-in secondary formatter.
//
// DEFERRED (Plan 4+): the write_kv_table body is not ported in the MVP; the type
// and Name exist so FromFormatName resolves. Format returns an empty rendering.
type MarkdownKvFormatter struct {
	IncludeDropSummary bool
}

// Name returns the stable tag "markdown-kv".
func (MarkdownKvFormatter) Name() string { return "markdown-kv" }

// EstimateBytes returns len(Format(c)) (the tested invariant).
func (f MarkdownKvFormatter) EstimateBytes(c *Compaction) int { return len(f.Format(c)) }

// Format is a DEFERRED (Plan 4+) stub: the KV rendering body is not ported in the
// MVP. It returns an empty string so the invariant EstimateBytes==len(Format)
// still holds.
func (f MarkdownKvFormatter) Format(c *Compaction) string { return "" }

// kvScalar renders a scalar for a KV value. Null -> "null" (literal, UNLIKE the
// CSV empty cell) — kept as a distinct helper so the CSV/KV null divergence is not
// accidentally unified. bool/number/string render like CSV without the comma
// heuristic; object/array -> compact JSON.
func kvScalar(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(val)
	case json.Number:
		return val.String()
	case string:
		return val
	case *orderedmap.OrderedMap, []any:
		return compactJSON(val)
	default:
		return compactJSON(val)
	}
}

// formatCcrMarker builds the COMMA-FORM opaque-cell marker <<ccr:HASH,KIND,SIZE>>.
//
// INTEGRATOR NOTE (Decision B): upstream Rust renders SIZE humanized (e.g. 512B,
// 4.5KB). The existing ccr.MarkerForCell(hash, kind, size int) renders SIZE as a
// raw int, and Decision B permits either. We KEEP ccr.MarkerForCell's int form
// (do NOT add a new builder) and assert the marker SHAPE in tests, not a byte-exact
// humanized SIZE. humanizeBytes stays available for the JSON _size / diagnostic
// path. HASH is whatever the IR cell carries (12-hex hashOpaque from the compactor
// for Tier-2 cells; 24-hex BLAKE3 for walker-emitted blobs).
func formatCcrMarker(hash string, byteSize int, kind OpaqueKind) string {
	return ccr.MarkerForCell(hash, kind.String(), byteSize)
}

// humanizeBytes renders a byte count as B/KB/MB with one decimal place: n<1024 ->
// "{n}B"; n/1024 < 1024 -> "{kb:.1}KB"; else "{mb:.1}MB". The 1024 boundary lands
// in the KB branch and 1024*1024 in the MB branch. Decision B: no exact-rounding
// parity is chased.
func humanizeBytes(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + "B"
	}
	kb := float64(n) / 1024.0
	if kb < 1024.0 {
		return strconv.FormatFloat(kb, 'f', 1, 64) + "KB"
	}
	mb := kb / 1024.0
	return strconv.FormatFloat(mb, 'f', 1, 64) + "MB"
}

// compactJSON marshals a decodeJSON-shaped value to compact JSON with ordered
// object keys and HTML escaping disabled. On failure it returns "" (mirrors the
// upstream unwrap_or_default fallback).
func compactJSON(v any) string {
	return marshalOrdered(v, false)
}

// marshalOrdered marshals v (orderedmap-aware) to JSON, compact or pretty. HTML
// escaping is disabled so '<'/'>'/'&' survive verbatim. On error it returns "".
func marshalOrdered(v any, pretty bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return ""
	}
	// Encoder appends a trailing newline; trim it for a compact single-line form.
	return strings.TrimRight(buf.String(), "\n")
}

// firstNonSpaceRune returns the first non-whitespace rune of s (0 if none). It is
// shared by the walker's container gate.
func firstNonSpaceRune(s string) rune {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return r
		}
	}
	return 0
}
