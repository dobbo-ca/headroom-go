package compaction

import (
	"strings"
	"testing"
)

// tableFromJSON compacts a JSON array source into a *Compaction so formatter
// tests can express table inputs concisely. It reuses the package-local ordered
// decoder (see classifier_test.go's decode helper).
func tableFromJSON(t *testing.T, s string) *Compaction {
	t.Helper()
	c := Compact(decodeArr(t, s), DefaultCompactConfig())
	return &c
}

func TestEstimateBytesEqualsFormatLen(t *testing.T) {
	// EstimateBytes MUST == len(Format(c)) exactly for BOTH formatters
	// [ref: formatter.rs edgeCases].
	c := tableFromJSON(t, `[{"id":1,"level":"info","msg":"a"},{"id":2,"level":"info","msg":"b"},{"id":3,"level":"warn","msg":"c"}]`)

	csv := CsvSchemaFormatter{}
	if got, want := csv.EstimateBytes(c), len(csv.Format(c)); got != want {
		t.Errorf("CsvSchemaFormatter.EstimateBytes = %d, want len(Format)=%d", got, want)
	}

	js := JsonFormatter{}
	if got, want := js.EstimateBytes(c), len(js.Format(c)); got != want {
		t.Errorf("JsonFormatter.EstimateBytes = %d, want len(Format)=%d", got, want)
	}
}

func TestCsvDeclarationRowCountIsKept(t *testing.T) {
	// [N] = rows.len() KEPT, NOT original_count. With no drops the two coincide;
	// assert the leading [N]{ token equals the kept row count.
	c := tableFromJSON(t, `[{"id":1,"level":"info"},{"id":2,"level":"info"},{"id":3,"level":"warn"}]`)
	out := CsvSchemaFormatter{}.Format(c)
	if !strings.HasPrefix(out, "[3]{") {
		t.Errorf("CSV declaration = %q, want prefix %q", out, "[3]{")
	}
	// One declaration line + one row line per kept row.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1+(*c).KeptRowCount() {
		t.Errorf("CSV line count = %d, want %d (1 decl + %d rows)", len(lines), 1+(*c).KeptRowCount(), (*c).KeptRowCount())
	}
}

func TestCsvMissingCellEmptyBetweenCommas(t *testing.T) {
	// A row lacking a column emits an empty CSV cell between commas (a,,c).
	// Objects [{a,b,c},{a,c}] -> column b is Missing in row 2. Column order is
	// DESC freq then ASC name: a(2),c(2),b(1) -> order a,c,b.
	c := tableFromJSON(t, `[{"a":"1","b":"2","c":"3"},{"a":"4","c":"6"}]`)
	out := CsvSchemaFormatter{}.Format(c)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// row 2 is "4,6," (a=4, c=6, b Missing -> empty trailing cell).
	last := lines[len(lines)-1]
	if last != "4,6," {
		t.Errorf("row with Missing cell = %q, want %q", last, "4,6,")
	}
}

func TestCsvNullScalarEmptyVsKvNullLiteral(t *testing.T) {
	// Null scalar -> CSV "" ; KV literal "null". Do NOT unify.
	if got := jsonScalarToCsv(nil); got != "" {
		t.Errorf("jsonScalarToCsv(nil) = %q, want \"\"", got)
	}
	if got := kvScalar(nil); got != "null" {
		t.Errorf("kvScalar(nil) = %q, want \"null\"", got)
	}
}

func TestCsvQuoteDoublesInternalQuotes(t *testing.T) {
	// csvQuote wraps in quotes and doubles internal double-quotes; newlines and
	// commas are literal inside the quotes.
	if got, want := csvQuote(`a"b`), `"a""b"`; got != want {
		t.Errorf("csvQuote(%q) = %q, want %q", `a"b`, got, want)
	}
	if got, want := csvQuote("a,b\nc"), "\"a,b\nc\""; got != want {
		t.Errorf("csvQuote newline/comma = %q, want %q", got, want)
	}
}

func TestNeedsCsvQuote(t *testing.T) {
	// needsCsvQuote iff the string contains , " \n or \r.
	cases := []struct {
		in   string
		want bool
	}{
		{"plain", false},
		{"a,b", true},
		{`a"b`, true},
		{"a\nb", true},
		{"a\rb", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := needsCsvQuote(tc.in); got != tc.want {
			t.Errorf("needsCsvQuote(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCsvNestedCellIsJsonQuoted(t *testing.T) {
	// A nested (sub-array-of-objects) cell renders as JsonFormatter output then
	// csvQuote'd. Two objects each holding a nested array of >=2 objects -> the
	// nested column cell is the quoted JSON of the sub-compaction.
	c := tableFromJSON(t, `[{"id":1,"rows":[{"x":1},{"x":2}]},{"id":2,"rows":[{"x":3},{"x":4}]}]`)
	out := CsvSchemaFormatter{}.Format(c)
	// The nested cell must be a CSV-quoted JsonFormatter rendering (contains the
	// _compaction JSON key, wrapped in doubled quotes because the JSON has commas).
	if !strings.Contains(out, `"{""_compaction""`) {
		t.Errorf("CSV nested cell not JSON-quoted; got %q", out)
	}
}

func TestFormatCcrMarkerCommaFormShape(t *testing.T) {
	// formatCcrMarker builds the comma-form <<ccr:HASH,KIND,SIZE>> via
	// ccr.MarkerForCell (int SIZE per the integrator note; assert SHAPE, not exact
	// humanized size). Hash is whatever the IR cell carries (12-hex from the
	// compactor here).
	m := formatCcrMarker("abc123def456", 512, Base64Blob)
	if !strings.HasPrefix(m, "<<ccr:abc123def456,base64,") {
		t.Errorf("marker = %q, want prefix <<ccr:abc123def456,base64,", m)
	}
	if !strings.HasSuffix(m, ">>") {
		t.Errorf("marker = %q, want suffix >>", m)
	}
	// Exactly two commas inside the marker body (HASH,KIND,SIZE).
	body := strings.TrimSuffix(strings.TrimPrefix(m, "<<ccr:"), ">>")
	if n := strings.Count(body, ","); n != 2 {
		t.Errorf("marker body %q has %d commas, want 2", body, n)
	}
}

func TestCompactJSONNoHTMLEscape(t *testing.T) {
	// compactJSON / marshalOrdered must leave '<', '>', '&' LITERAL, including
	// inside nested object string values (where orderedmap's MarshalJSON would
	// otherwise HTML-escape them despite the outer SetEscapeHTML(false)).
	cases := []struct {
		name string
		json string
		want string
	}{
		{"bare string", `"x < y & z > w"`, `"x < y & z > w"`},
		{"nested object value", `{"a":"x < y & z > w"}`, `{"a":"x < y & z > w"}`},
		{"array of objects", `[{"a":"p<q"},{"a":"r&s"},{"a":"t>u"}]`, `[{"a":"p<q"},{"a":"r&s"},{"a":"t>u"}]`},
		{"embedded ccr marker", `{"_ccr_dropped":"<<ccr:abc 5_rows_offloaded>>"}`, `{"_ccr_dropped":"<<ccr:abc 5_rows_offloaded>>"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compactJSON(decode(t, c.json)); got != c.want {
				t.Errorf("compactJSON(%s) = %q, want %q", c.json, got, c.want)
			}
		})
	}
}

func TestHumanizeBytesBoundaries(t *testing.T) {
	// n<1024 -> "{n}B"; n==1024 -> KB branch; n==1024*1024 -> MB branch; 1 decimal.
	cases := []struct {
		n    int
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1024 * 1024, "1.0MB"},
	}
	for _, tc := range cases {
		if got := humanizeBytes(tc.n); got != tc.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
