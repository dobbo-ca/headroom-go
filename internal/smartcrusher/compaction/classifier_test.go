package compaction

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// decode wraps the package-local ordered decoder (jsonutil.go) so table tests can
// express object/array inputs as JSON source. Using the production decoder keeps
// compaction free of an import cycle back to its parent smartcrusher package.
func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		t.Fatalf("decode(%q) error: %v", s, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("decode(%q): trailing data or error: %v", s, err)
	}
	return v
}

// kindOf returns the kind label so table tests read cleanly.
func kindOf(c CellClass) CellClassKind { return c.Kind }

func TestClassifyCellObjectAndArray(t *testing.T) {
	cfg := DefaultClassifyConfig()

	obj := decode(t, `{"a":1}`)
	if got := classifyCell(obj, cfg); kindOf(got) != ClassJsonObject {
		t.Errorf("classifyCell(object) kind = %v, want JsonObject", kindOf(got))
	}

	arr := decode(t, `[1,2,3]`)
	if got := classifyCell(arr, cfg); kindOf(got) != ClassJsonArray {
		t.Errorf("classifyCell(array) kind = %v, want JsonArray", kindOf(got))
	}
}

func TestClassifyCellShortStringScalar(t *testing.T) {
	cfg := DefaultClassifyConfig()
	if got := classifyCell("hello", cfg); kindOf(got) != ClassScalar {
		t.Errorf("classifyCell(short string) kind = %v, want Scalar", kindOf(got))
	}
}

func TestClassifyCellStringifiedJSONLeadingWhitespace(t *testing.T) {
	// '  {"a":1}' -> StringifiedJson: gate trims leading whitespace for the first-
	// char check, but parse operates on the ORIGINAL untrimmed string. Use a small
	// opaque_min_bytes override so a short payload still reaches the string path
	// (the stringified-JSON check runs BEFORE the length gate anyway, but keep the
	// override to exercise configurability).
	cfg := DefaultClassifyConfig()
	cfg.OpaqueMinBytes = 10
	got := classifyCell(`  {"a":1}`, cfg)
	if kindOf(got) != ClassStringifiedJson {
		t.Fatalf("classifyCell('  {\"a\":1}') kind = %v, want StringifiedJson", kindOf(got))
	}
	if got.Parsed == nil {
		t.Errorf("StringifiedJson result carries nil Parsed; want the decoded value")
	}
	if _, ok := got.Parsed.(*orderedmap.OrderedMap); !ok {
		t.Errorf("StringifiedJson Parsed = %T, want *orderedmap.OrderedMap", got.Parsed)
	}
}

func TestClassifyCellStringifiedJSONArray(t *testing.T) {
	// A STRING whose content is a JSON array -> StringifiedJson carrying a []any.
	cfg := DefaultClassifyConfig()
	cfg.OpaqueMinBytes = 10
	got := classifyCell(`[1,2]`, cfg)
	if kindOf(got) != ClassStringifiedJson {
		t.Fatalf("classifyCell(\"[1,2]\") kind = %v, want StringifiedJson", kindOf(got))
	}
	if _, ok := got.Parsed.([]any); !ok {
		t.Errorf("StringifiedJson Parsed = %T, want []any", got.Parsed)
	}
}

func TestClassifyCellBareScalarNotStringifiedJSON(t *testing.T) {
	// "123" and "true" parse as JSON but fail the '{'/'[' first-char gate, so they
	// are NOT StringifiedJson; short -> Scalar [ref: classifier.rs edgeCases].
	cfg := DefaultClassifyConfig()
	cfg.OpaqueMinBytes = 10
	for _, s := range []string{"123", "true", "null"} {
		if got := classifyCell(s, cfg); kindOf(got) != ClassScalar {
			t.Errorf("classifyCell(%q) kind = %v, want Scalar", s, kindOf(got))
		}
	}
}

func TestClassifyCellLongStringPadded(t *testing.T) {
	// >256 low-diversity padded (parse fails, base64 fails low diversity, html no
	// tags) -> Opaque(LongString) [ref: classifier.rs edgeCases].
	s := strings.Repeat("ab ", 100) // 300 bytes, whitespace disqualifies base64, no tags
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(padded) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != LongString.String() {
		t.Errorf("classifyCell(padded) OpaqueKind = %q, want %q", got.OpaqueKind.String(), LongString.String())
	}
}

func TestClassifyCellBase64Blob(t *testing.T) {
	// A valid base64 string >=64 bytes with >=16 unique chars, no '<'/'>'/whitespace,
	// and >=95% base64 alphabet -> Opaque(Base64Blob).
	s := strings.Repeat("ABCDefgh1234+/wxYZ0987", 20) // long, diverse, all base64 alphabet
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(base64) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != Base64Blob.String() {
		t.Errorf("classifyCell(base64) OpaqueKind = %q, want %q", got.OpaqueKind.String(), Base64Blob.String())
	}
}

func TestClassifyCellBase64RejectedByAngleBracket(t *testing.T) {
	// base64-looking but contains '<' -> reject base64; falls to LongString
	// (still >256, parse/html fail).
	s := "<" + strings.Repeat("ABCDefgh1234+/wxYZ0987", 20)
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(base64+angle) kind = %v, want Opaque", kindOf(got))
	}
	// A single '<' at the front is one tag-open but not 3, so html fails too.
	if got.OpaqueKind.String() != LongString.String() {
		t.Errorf("classifyCell(base64+angle) OpaqueKind = %q, want %q", got.OpaqueKind.String(), LongString.String())
	}
}

func TestClassifyCellBase64RejectedByWhitespace(t *testing.T) {
	// base64-looking but contains whitespace -> reject base64; falls to LongString.
	s := strings.Repeat("ABCDefgh1234+/wxYZ0987", 10) + " " + strings.Repeat("ABCDefgh1234+/wxYZ0987", 10)
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(base64+ws) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != LongString.String() {
		t.Errorf("classifyCell(base64+ws) OpaqueKind = %q, want %q", got.OpaqueKind.String(), LongString.String())
	}
}

func TestClassifyCellHtmlChunk(t *testing.T) {
	// opens>=3 AND tag_starts>=3 -> Opaque(HtmlChunk). Pad past 256 bytes so the
	// length gate passes; base64 fails (contains '<'), html wins.
	s := "<div><span><p>" + strings.Repeat("content here ", 30) + "</p></span></div>"
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(html) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != HtmlChunk.String() {
		t.Errorf("classifyCell(html) OpaqueKind = %q, want %q", got.OpaqueKind.String(), HtmlChunk.String())
	}
}

func TestClassifyCellAngleBracketNotHtml(t *testing.T) {
	// 'a < b' padded past 256: a single '<' whose next byte is a space is NOT a
	// tag-start, so html fails (needs opens>=3 AND tag_starts>=3) -> LongString.
	s := "a < b " + strings.Repeat("plain text ", 30)
	cfg := DefaultClassifyConfig()
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell('a < b'...) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != LongString.String() {
		t.Errorf("classifyCell('a < b'...) OpaqueKind = %q, want %q", got.OpaqueKind.String(), LongString.String())
	}
}

func TestClassifyCellLengthGateByteLen(t *testing.T) {
	// The length gate uses BYTE length. A short (<=256 byte) non-container string
	// stays Scalar even if it looks base64-ish.
	s := strings.Repeat("A", 200) // 200 bytes, below the 256 default gate
	cfg := DefaultClassifyConfig()
	if got := classifyCell(s, cfg); kindOf(got) != ClassScalar {
		t.Errorf("classifyCell(200-byte string) kind = %v, want Scalar (length gate)", kindOf(got))
	}
}

func TestClassifyCellOpaqueMinBytesConfigurable(t *testing.T) {
	// opaque_min_bytes configurable down to 10: a padded low-diversity string just
	// over 10 bytes becomes Opaque(LongString).
	s := strings.Repeat("ab ", 5) // 15 bytes, > 10
	cfg := DefaultClassifyConfig()
	cfg.OpaqueMinBytes = 10
	got := classifyCell(s, cfg)
	if kindOf(got) != ClassOpaque {
		t.Fatalf("classifyCell(15-byte, min=10) kind = %v, want Opaque", kindOf(got))
	}
	if got.OpaqueKind.String() != LongString.String() {
		t.Errorf("classifyCell(15-byte, min=10) OpaqueKind = %q, want %q", got.OpaqueKind.String(), LongString.String())
	}
}
