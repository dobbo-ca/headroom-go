package compaction

// This file is the compaction submodule's stage glue [ref: compaction/mod.rs]. It
// binds a CompactConfig to a Formatter and exposes Run, which compacts an array
// and renders it in one call. It holds NO compaction logic, markers, or float math.
//
// CompactionStage carries ONLY {Config, Formatter} — deliberately NO ccr.Store
// field. The store-carrying type is DocumentCompactor (walker.go); opaque CELLS
// produced inside a Tier-2 lossless table are rendered for byte-saving but are
// intentionally store-orphaned in the MVP (see the "Opaque-cell hash + store-write"
// contract in the plan). Round-trippable opaque blobs go through the walker.

// CompactionStage pairs a compaction config with a renderer.
type CompactionStage struct {
	Config    CompactConfig
	Formatter Formatter
}

// DefaultCsvSchemaStage builds a stage with the CSV-schema formatter and the OSS
// default compaction config.
func DefaultCsvSchemaStage() CompactionStage {
	return CompactionStage{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}
}

// CsvSchemaStage builds a CSV-schema stage with an explicit config (the crusher's
// constructor uses this).
func CsvSchemaStage(config CompactConfig) CompactionStage {
	return CompactionStage{Config: config, Formatter: CsvSchemaFormatter{}}
}

// DefaultJSONStage builds a stage with the JSON formatter and the default config.
func DefaultJSONStage() CompactionStage {
	return CompactionStage{Config: DefaultCompactConfig(), Formatter: JsonFormatter{}}
}

// DefaultMarkdownKVStage builds a stage with the markdown-kv formatter and the
// default config.
func DefaultMarkdownKVStage() CompactionStage {
	return CompactionStage{Config: DefaultCompactConfig(), Formatter: MarkdownKvFormatter{}}
}

// SupportedFormatNames lists the format names in canonical order. It is the single
// source of truth, synced with FromFormatName.
var SupportedFormatNames = []string{"csv-schema", "json", "markdown-kv"}

// FromFormatName resolves a format name (exact, case-sensitive) to a stage; unknown
// names return (nil, false) so the caller owns the fallback (no silent default).
func FromFormatName(name string) (*CompactionStage, bool) {
	var stage CompactionStage
	switch name {
	case "csv-schema":
		stage = DefaultCsvSchemaStage()
	case "json":
		stage = DefaultJSONStage()
	case "markdown-kv":
		stage = DefaultMarkdownKVStage()
	default:
		return nil, false
	}
	return &stage, true
}

// Run compacts items and renders the result, returning both the IR tree (so
// callers can read kept/total counts) and the rendered bytes.
func (s *CompactionStage) Run(items []any) (*Compaction, string) {
	c := Compact(items, s.Config)
	rendered := s.Formatter.Format(&c)
	return &c, rendered
}
