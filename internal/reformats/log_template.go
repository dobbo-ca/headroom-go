package reformats

import (
	"fmt"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// logTemplateName is the transform name, stamped into every wrapped error.
const logTemplateName = "log_template"

// wildcard is the literal sentinel emitted in a template header for each varying
// (wildcard) position. Variant rows carry the actual per-line token instead.
const wildcard = "<*>"

// LogTemplateConfig holds the four tuning knobs. Defaults (upstream
// config/pipeline.toml [reformat.log_template]): minLines=20, minRun=3,
// similarityThreshold=0.4, minConstantTokens=2.
type LogTemplateConfig struct {
	MinLines            int
	MinRun              int
	SimilarityThreshold float32
	MinConstantTokens   int
}

// defaultLogTemplateConfig returns the upstream default knobs.
func defaultLogTemplateConfig() LogTemplateConfig {
	return LogTemplateConfig{
		MinLines:            20,
		MinRun:              3,
		SimilarityThreshold: 0.4,
		MinConstantTokens:   2,
	}
}

// LogTemplate is a lossless log run-collapsing reformat implementing
// transform.ReformatTransform. It mines consecutive same-shaped lines into a
// template header plus a variant table, discovering variability purely
// positionally (a token position becomes a wildcard "<*>" only because it
// differed across the run) — there is NO masking regex. The original line equals
// the template with each wildcard replaced, in order, by the row's variant tokens.
//
// A zero-value LogTemplate uses the default config knobs.
type LogTemplate struct {
	config *LogTemplateConfig
}

// cfg returns the effective config, defaulting when unset (zero-value support).
func (lt LogTemplate) cfg() LogTemplateConfig {
	if lt.config != nil {
		return *lt.config
	}
	return defaultLogTemplateConfig()
}

// Name returns the transform name "log_template".
func (LogTemplate) Name() string { return logTemplateName }

// AppliesTo returns the single content type this reformat applies to.
func (LogTemplate) AppliesTo() []transform.ContentType {
	return []transform.ContentType{transform.BuildOutput}
}

// run is an in-progress mined run of consecutive same-shaped lines. indices are
// the original line indices; template[pos] is a slot: a constant token (wild=false)
// or a wildcard (wild=true).
type run struct {
	indices  []int
	template []slot
}

// slot is one template position: a constant token, or a wildcard.
type slot struct {
	tok  string
	wild bool
}

// Apply mines consecutive same-template runs and emits template-header + variant
// rows for collapsible runs (or the lines verbatim). It is lossless and applies a
// never-inflate guard against the RAW content byte length.
func (lt LogTemplate) Apply(content string) (transform.ReformatOutput, error) {
	cfg := lt.cfg()

	// GUARD 1: empty input.
	if content == "" {
		return transform.ReformatOutput{}, fmt.Errorf("%s skipped: empty input: %w", logTemplateName, transform.ErrSkipped)
	}

	// SPLIT LINES with Rust str::lines() semantics; track trailing newline.
	lines := splitLinesRust(content)
	endsWithNewline := strings.HasSuffix(content, "\n")

	// GUARD 2: below min_lines (line count, not bytes).
	if len(lines) < cfg.MinLines {
		return transform.ReformatOutput{}, fmt.Errorf("%s skipped: input below min_lines: %w", logTemplateName, transform.ErrSkipped)
	}

	// TOKENIZE ALL: strings.Fields == Rust split_whitespace (no empty tokens).
	tokenized := make([][]string, len(lines))
	for i, line := range lines {
		tokenized[i] = strings.Fields(line)
	}

	var out strings.Builder
	out.Grow(len(content))
	nextTemplateID := 1
	var active *run

	for i, toks := range tokenized {
		if len(toks) == 0 {
			// CASE A: blank/whitespace-only line — flush active run, emit verbatim.
			if active != nil {
				flushRun(active, lines, tokenized, cfg, &nextTemplateID, &out)
				active = nil
			}
			out.WriteString(lines[i])
			out.WriteByte('\n')
			continue
		}
		if active != nil && extendsRun(active, toks, cfg.SimilarityThreshold) {
			// CASE B: extend the current run.
			active.indices = append(active.indices, i)
			mergeIntoTemplate(active.template, toks)
			continue
		}
		// CASE C: flush any active run, then start a fresh one.
		if active != nil {
			flushRun(active, lines, tokenized, cfg, &nextTemplateID, &out)
		}
		active = startRun(i, toks)
	}
	// END-OF-INPUT FLUSH.
	if active != nil {
		flushRun(active, lines, tokenized, cfg, &nextTemplateID, &out)
	}

	result := out.String()

	// TRAILING-NEWLINE FIXUP: if input did NOT end with '\n', drop the final one.
	if !endsWithNewline && strings.HasSuffix(result, "\n") {
		result = result[:len(result)-1]
	}

	// NEVER-INFLATE GUARD (byte length, inclusive >=): fall back to RAW content.
	if len(result) >= len(content) {
		return transform.ReformatOutput{Output: content, BytesSaved: 0}, nil
	}
	return transform.ReformatOutput{Output: result, BytesSaved: len(content) - len(result)}, nil
}

// startRun begins a fresh run: indices=[i], every token a constant slot.
func startRun(i int, toks []string) *run {
	tmpl := make([]slot, len(toks))
	for p, t := range toks {
		tmpl[p] = slot{tok: t, wild: false}
	}
	return &run{indices: []int{i}, template: tmpl}
}

// extendsRun reports whether toks extends r under similarity threshold. Token
// count must match exactly; wildcard positions count as matches.
func extendsRun(r *run, toks []string, sim float32) bool {
	if len(toks) != len(r.template) {
		return false
	}
	matches := 0
	for pos, t := range toks {
		s := r.template[pos]
		if s.wild || s.tok == t {
			matches++
		}
	}
	return float32(matches)/float32(len(toks)) >= sim
}

// mergeIntoTemplate demotes any constant position that differs from toks to a
// wildcard. Lengths are guaranteed equal (extendsRun gated on token count).
func mergeIntoTemplate(template []slot, toks []string) {
	for pos := range template {
		if !template[pos].wild && template[pos].tok != toks[pos] {
			template[pos].wild = true
		}
	}
}

// flushRun emits r either as a collapsed template-header + variant table, or as
// verbatim lines, per the three-part collapse gate.
func flushRun(r *run, lines []string, tokenized [][]string, cfg LogTemplateConfig, nextTemplateID *int, out *strings.Builder) {
	constant := 0
	for _, s := range r.template {
		if !s.wild {
			constant++
		}
	}
	varying := len(r.template) - constant

	collapse := len(r.indices) >= cfg.MinRun && constant >= cfg.MinConstantTokens && varying > 0
	if !collapse {
		for _, i := range r.indices {
			out.WriteString(lines[i])
			out.WriteByte('\n')
		}
		return
	}

	templateID := *nextTemplateID
	*nextTemplateID++

	// Header: "[Template T<id>: <slots>] (<N> occurrences)\n".
	out.WriteString("[Template T")
	fmt.Fprintf(out, "%d", templateID)
	out.WriteString(": ")
	for pos, s := range r.template {
		if pos > 0 {
			out.WriteByte(' ')
		}
		if s.wild {
			out.WriteString(wildcard)
		} else {
			out.WriteString(s.tok)
		}
	}
	out.WriteString("] (")
	fmt.Fprintf(out, "%d", len(r.indices))
	out.WriteString(" occurrences)\n")

	// Variant table: one row per index, only wildcard-position tokens (ascending).
	for _, i := range r.indices {
		first := true
		for pos, s := range r.template {
			if !s.wild {
				continue
			}
			if !first {
				out.WriteByte(' ')
			}
			out.WriteString(tokenized[i][pos])
			first = false
		}
		out.WriteByte('\n')
	}
}
