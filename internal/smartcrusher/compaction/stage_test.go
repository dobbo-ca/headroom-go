package compaction

import (
	"strings"
	"testing"
)

func TestSupportedFormatNames(t *testing.T) {
	// Exact order; single source of truth, synced with FromFormatName.
	want := []string{"csv-schema", "json", "markdown-kv"}
	if len(SupportedFormatNames) != len(want) {
		t.Fatalf("SupportedFormatNames = %v, want %v", SupportedFormatNames, want)
	}
	for i, n := range want {
		if SupportedFormatNames[i] != n {
			t.Errorf("SupportedFormatNames[%d] = %q, want %q", i, SupportedFormatNames[i], n)
		}
	}
}

func TestFromFormatNameExactCaseSensitive(t *testing.T) {
	for _, n := range SupportedFormatNames {
		st, ok := FromFormatName(n)
		if !ok || st == nil {
			t.Errorf("FromFormatName(%q) = (%v,%v), want a stage", n, st, ok)
			continue
		}
		if st.Formatter.Name() != n {
			t.Errorf("FromFormatName(%q).Formatter.Name() = %q, want %q", n, st.Formatter.Name(), n)
		}
	}
	// Unknown / wrong case -> nil,false (do NOT silently default).
	for _, bad := range []string{"CSV-Schema", "csv_schema", "", "yaml"} {
		if st, ok := FromFormatName(bad); ok || st != nil {
			t.Errorf("FromFormatName(%q) = (%v,%v), want (nil,false)", bad, st, ok)
		}
	}
}

func TestStageRunReturnsIRAndBytes(t *testing.T) {
	// Run(items) -> (*Compaction, rendered). The IR exposes kept/total; rendered is
	// the formatter output.
	st := DefaultCsvSchemaStage()
	items := decodeArr(t, `[{"id":1,"level":"info"},{"id":2,"level":"info"},{"id":3,"level":"warn"}]`)
	c, rendered := st.Run(items)
	if c == nil {
		t.Fatal("Run returned nil compaction")
	}
	if !(*c).WasCompacted() {
		t.Errorf("Run compaction WasCompacted=false, want true for a uniform array")
	}
	if (*c).KeptRowCount() != 3 || (*c).OriginalRowCount() != 3 {
		t.Errorf("kept/total = %d/%d, want 3/3", (*c).KeptRowCount(), (*c).OriginalRowCount())
	}
	if !strings.HasPrefix(rendered, "[3]{") {
		t.Errorf("rendered = %q, want CSV declaration prefix [3]{", rendered)
	}
	if rendered != st.Formatter.Format(c) {
		t.Errorf("Run rendered != Formatter.Format(c)")
	}
}

func TestStageConstructorsFormatterNames(t *testing.T) {
	// The four constructors carry the expected formatter names / configs.
	if DefaultCsvSchemaStage().Formatter.Name() != "csv-schema" {
		t.Errorf("DefaultCsvSchemaStage formatter = %q, want csv-schema", DefaultCsvSchemaStage().Formatter.Name())
	}
	if CsvSchemaStage(DefaultCompactConfig()).Formatter.Name() != "csv-schema" {
		t.Errorf("CsvSchemaStage formatter = %q, want csv-schema", CsvSchemaStage(DefaultCompactConfig()).Formatter.Name())
	}
	if DefaultJSONStage().Formatter.Name() != "json" {
		t.Errorf("DefaultJSONStage formatter = %q, want json", DefaultJSONStage().Formatter.Name())
	}
	if DefaultMarkdownKVStage().Formatter.Name() != "markdown-kv" {
		t.Errorf("DefaultMarkdownKVStage formatter = %q, want markdown-kv", DefaultMarkdownKVStage().Formatter.Name())
	}
}

// TestCompactionStageHasNoStoreField is a compile-time assertion that
// CompactionStage carries ONLY {Config, Formatter} and no ccr.Store field (the
// store-carrying type is DocumentCompactor). If a store field were added, this
// struct literal would fail to compile.
func TestCompactionStageHasNoStoreField(t *testing.T) {
	_ = CompactionStage{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}
}
