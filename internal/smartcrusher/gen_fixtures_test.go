//go:build genfixtures

package smartcrusher

// This file is a one-shot fixture GENERATOR, not part of the acceptance suite. It
// is guarded by the "genfixtures" build tag so it never runs in the normal test
// pass. To (re)materialize testdata/fixtures/*.json run:
//
//	go test -tags genfixtures ./internal/smartcrusher/ -run TestGenerateFixtures -v
//
// It records each fixture's input `content`, path, config overrides, WasModified,
// and the recorded compressed length so the acceptance harness can ratio-bound
// against it. The recorded lengths are DERIVED from the current CORRECT behavior
// (Decision B), not from upstream byte output.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func genLossyLen(content string, cfg Config) (int, bool) {
	sc := NewSmartCrusherWithoutCompaction(cfg)
	crushed, mod, _ := sc.smartCrushContent(content, "", 0.0)
	return len(crushed), mod
}

func genDefaultLen(content string, cfg Config) (int, bool) {
	sc := NewSmartCrusher(cfg)
	crushed, mod, _ := sc.smartCrushContent(content, "", 0.0)
	return len(crushed), mod
}

func genDocLen(content string) (int, bool) {
	sc := NewSmartCrusher(DefaultConfig())
	out, err := sc.CompactDocumentJSON(content)
	if err != nil {
		panic(err)
	}
	// A document fixture "wasModified" is defined as its serialized form differing
	// from the trimmed input.
	return len(out), out != strings.TrimSpace(content)
}

func writeFixture(t *testing.T, f fixtureFile) {
	t.Helper()
	dir := filepath.Join("testdata", "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join(dir, f.Name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("wrote %s (compressedLen=%d wasModified=%v)\n", path, f.RecordedCompressedLen, f.WasModified)
}

func TestGenerateFixtures(t *testing.T) {
	// --- content builders (spaced input so reserialize flips WasModified) -------

	// dict_array_30: 30 dicts {id,status,msg}, every 5th an error row.
	dictArray30 := buildDictArray30()
	// dict_array_100_sequential: level='warn' every 7th.
	dictArray100 := buildDictArray100()
	// duplicate_dicts_40: 40 identical dicts (dedup -> 1).
	dup40 := buildDuplicateDicts40()
	// string_array_25 (DEFERRED string crusher -> ratio-bound only).
	stringArray25 := buildStringArray25()
	// number_array_40_changepoint (DEFERRED number crusher -> ratio-bound only).
	numberArray40 := buildNumberArray40()
	// mixed_array (DEFERRED mixed crusher -> ratio-bound only).
	mixedArray := buildMixedArray()
	// nested_object_with_array: events -> skip:unique_entities_no_signal.
	nested := buildNestedObjectWithArray()
	// uniform tabular (lossless:table default).
	uniform50 := buildUniformDicts(50)
	uniform50Spaced := spaceOutArray(uniform50)
	// below-threshold: 30 unique dicts (lossless declines? or lossy) -> never passthrough.
	below := buildBelowThreshold30()
	// CCR-marker-visible (ratio 0.99 default -> lossy space-form in output).
	// reuse dictArray30 content, default ctor, gate 0.99.
	// opaque blob in object (walker path).
	opaqueDoc := buildOpaqueBlobDoc()
	// compact_document_json (blob + tabular sub-array).
	compactDoc := buildCompactDocument()

	lossy99 := DefaultConfig()
	lossy99.LosslessMinSavingsRatio = 0.99

	fixtures := []fixtureFile{
		func() fixtureFile {
			l, m := genLossyLen("[]", DefaultConfig())
			return fixtureFile{Name: "empty_array", Path: "lossy", Content: "[]", WasModified: m, RecordedCompressedLen: l, ByteExact: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen("[1, 2, 3]", DefaultConfig())
			return fixtureFile{Name: "short_array_passthrough", Path: "lossy", Content: "[1, 2, 3]", WasModified: m, RecordedCompressedLen: l, ByteExact: false, RecordedCompressed: "[1,2,3]"}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(dictArray30, DefaultConfig())
			return fixtureFile{Name: "dict_array_30", Path: "lossy", Content: dictArray30, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, KeptSubsetInline: true, HasDropSentinel: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(dictArray100, DefaultConfig())
			return fixtureFile{Name: "dict_array_100_sequential", Path: "lossy", Content: dictArray100, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, KeptSubsetInline: true, HasDropSentinel: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(dup40, DefaultConfig())
			return fixtureFile{Name: "duplicate_dicts_40", Path: "lossy", Content: dup40, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, KeptSubsetInline: true, HasDropSentinel: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(stringArray25, DefaultConfig())
			return fixtureFile{Name: "string_array_25", Path: "lossy", Content: stringArray25, WasModified: m, RecordedCompressedLen: l, RatioBoundOnly: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(numberArray40, DefaultConfig())
			return fixtureFile{Name: "number_array_40_changepoint", Path: "lossy", Content: numberArray40, WasModified: m, RecordedCompressedLen: l, RatioBoundOnly: true, AllNumeric: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(mixedArray, DefaultConfig())
			return fixtureFile{Name: "mixed_array", Path: "lossy", Content: mixedArray, WasModified: m, RecordedCompressedLen: l, RatioBoundOnly: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(nested, DefaultConfig())
			return fixtureFile{Name: "nested_object_with_array", Path: "lossy", Content: nested, WasModified: m, RecordedCompressedLen: l, SkipReason: "skip:unique_entities_no_signal"}
		}(),
		func() fixtureFile {
			l, m := genDefaultLen(uniform50Spaced, DefaultConfig())
			return fixtureFile{Name: "lossless_default_table", Path: "default", Content: uniform50Spaced, WasModified: m, RecordedCompressedLen: l, StrategyPrefix: "lossless:table"}
		}(),
		func() fixtureFile {
			l, m := genLossyLen(below, DefaultConfig())
			return fixtureFile{Name: "below_threshold_no_passthrough", Path: "lossy", Content: below, WasModified: m, RecordedCompressedLen: l, NeverPassthrough: true}
		}(),
		func() fixtureFile {
			l, m := genDefaultLen(dictArray30, lossy99)
			return fixtureFile{Name: "ccr_marker_visible", Path: "default", Content: dictArray30, Config: &fixtureConfig{LosslessMinSavingsRatio: ptrF(0.99)}, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, MarkerVisible: true, HasDropSentinel: true, KeptSubsetInline: true}
		}(),
		func() fixtureFile {
			l, m := genDocLen(opaqueDoc)
			return fixtureFile{Name: "opaque_blob_in_object", Path: "document", Content: opaqueDoc, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, BlobMarker: true}
		}(),
		func() fixtureFile {
			l, m := genDocLen(compactDoc)
			return fixtureFile{Name: "compact_document_json", Path: "document", Content: compactDoc, WasModified: m, RecordedCompressedLen: l, AssertRoundTrip: true, BlobMarker: true, SubArrayToString: true}
		}(),
		// Extra fixtures to reach >=17 and broaden coverage:
		func() fixtureFile {
			l, m := genLossyLen("[1,2,3]", DefaultConfig())
			return fixtureFile{Name: "short_array_compact_unmodified", Path: "lossy", Content: "[1,2,3]", WasModified: m, RecordedCompressedLen: l, ByteExact: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen("\"hello world\"", DefaultConfig())
			return fixtureFile{Name: "scalar_string_passthrough", Path: "lossy", Content: "\"hello world\"", WasModified: m, RecordedCompressedLen: l, ByteExact: true}
		}(),
		func() fixtureFile {
			l, m := genLossyLen("42", DefaultConfig())
			return fixtureFile{Name: "scalar_number_passthrough", Path: "lossy", Content: "42", WasModified: m, RecordedCompressedLen: l, ByteExact: true}
		}(),
	}

	for _, f := range fixtures {
		writeFixture(t, f)
	}
	fmt.Printf("total fixtures: %d\n", len(fixtures))
}

func ptrF(v float64) *float64 { return &v }

// ---- content builders shared by generator (spaced where reserialize matters) ---

func buildDictArray30() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		status := "ok"
		msg := "request handled"
		if i%5 == 0 {
			status = "error"
			msg = "connection failed"
		}
		fmt.Fprintf(&b, "  {\"id\": %d, \"status\": %q, \"msg\": %q}", i, status, msg)
	}
	b.WriteString("\n]")
	return b.String()
}

func buildDictArray100() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		level := "info"
		if i%7 == 0 {
			level = "warn"
		}
		fmt.Fprintf(&b, "  {\"id\": %d, \"level\": %q, \"msg\": \"log line here for volume\"}", i, level)
	}
	b.WriteString("\n]")
	return b.String()
}

func buildDuplicateDicts40() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("  {\"status\": \"ok\", \"code\": 200, \"msg\": \"same\"}")
	}
	b.WriteString("\n]")
	return b.String()
}

func buildStringArray25() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 25; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "  \"log entry number %d with some text\"", i)
	}
	b.WriteString("\n]")
	return b.String()
}

func buildNumberArray40() string {
	var b strings.Builder
	b.WriteString("[\n  ")
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		v := 10 + i
		if i >= 20 {
			v = 1000 + i
		}
		fmt.Fprintf(&b, "%d", v)
	}
	b.WriteString("\n]")
	return b.String()
}

func buildMixedArray() string {
	var b strings.Builder
	b.WriteString("[\n  ")
	for i := 0; i < 27; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		if i%3 == 0 {
			fmt.Fprintf(&b, "\"str %d\"", i)
		} else {
			fmt.Fprintf(&b, "%d", i)
		}
	}
	b.WriteString("\n]")
	return b.String()
}

func buildNestedObjectWithArray() string {
	var b strings.Builder
	b.WriteString("{\n  \"summary\": \"report\",\n  \"events\": [\n")
	for i := 0; i < 20; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "    {\"uid\": \"u-%d-%d\", \"name\": \"entity-%d\", \"payload\": \"data-%d-abc\"}", i, i*17, i, i)
	}
	b.WriteString("\n  ]\n}")
	return b.String()
}

func buildBelowThreshold30() string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "  {\"uid\": \"x-%d-%d\", \"blob\": \"unique-payload-%d-%d\"}", i, i*13, i, i*7)
	}
	b.WriteString("\n]")
	return b.String()
}

// spaceOutArray reformats a compact JSON array by inserting a space after each
// comma so the on-disk fixture is "spaced" and reserialization flips WasModified.
func spaceOutArray(compact string) string {
	return strings.ReplaceAll(compact, ",", ", ")
}

func buildOpaqueBlobDoc() string {
	blob := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk", 5)
	return fmt.Sprintf("{\n  \"id\": 1,\n  \"attachment\": %q,\n  \"note\": \"hello\"\n}", blob)
}

func buildCompactDocument() string {
	blob := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk", 5)
	var b strings.Builder
	b.WriteString("{\n  \"attachment\": \"" + blob + "\",\n  \"events\": [\n")
	for i := 0; i < 10; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "    {\"id\": %d, \"level\": \"info\", \"msg\": \"ok\"}", i)
	}
	b.WriteString("\n  ]\n}")
	return b.String()
}
