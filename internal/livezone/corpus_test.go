//go:build corpus

// Corpus harness for hr-tio. NOT part of the ordinary suite: it reads a
// transcript-derived corpus off disk and writes a result file for analysis.
//
//	go test -tags corpus ./internal/livezone/ -run TestCorpusClassify -timeout 60m
//
// Input  (TIO_IN,  default /tmp/tio/blocks.jsonl):  {tool,origin,sha,len,text}
// Output (TIO_OUT, default /tmp/tio/results.jsonl): the same minus text, plus
// what detect said and what the dispatcher did.
package livezone

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/policy"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/dobbo-ca/headroom-go/internal/transform"
	"github.com/tidwall/gjson"
)

type inBlock struct {
	Tool string `json:"tool"`
	// Cmd is the shell command for a Bash-family tool. The read-protection
	// gate needs it: `cat foo.rb` is a file read even though the tool is Bash.
	Cmd         string `json:"cmd"`
	Origin      string `json:"origin"`
	Sha         string `json:"sha"`
	Shape       string `json:"shape"`
	WireBytes   int    `json:"wire_bytes"`
	Occurrences int    `json:"occurrences"`
	Reshaped    string `json:"reshaped"`
	Wire        string `json:"wire"`
}

type outBlock struct {
	Tool     string `json:"tool"`
	Origin   string `json:"origin"`
	Sha      string `json:"sha"`
	Len      int    `json:"len"`
	Detected string `json:"detected"`
	Reason   string `json:"reason"`
	Action   string `json:"action"`
	Strategy string `json:"strategy"`
	Before   int    `json:"before"`
	After    int    `json:"after"`
}

// corpusBody wraps one block as the sole tool_result of the latest user
// message. This is the honest benchmark: a real block, at FrozenCount 0, on the
// only surface the dispatcher can reach.
func corpusBody(text string) []byte { return corpusBodyFor(text, "", "") }

// corpusBodyFor also carries the tool_use that PRODUCED the block, so the
// dispatcher can resolve the producing tool. Without it every block looks
// like it came from an unknown tool and the read-protection gate never fires —
// which measures an upper bound the production path would never reach.
func corpusBodyFor(text, tool, command string) []byte {
	quoted, err := json.Marshal(text)
	if err != nil {
		return nil
	}
	input := `{}`
	if command != "" {
		c, err := json.Marshal(command)
		if err != nil {
			return nil
		}
		input = `{"command":` + string(c) + `}`
	}
	name, err := json.Marshal(tool)
	if err != nil {
		return nil
	}
	return []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":` +
		string(name) + `,"input":` + input + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` +
		string(quoted) + `}]}]}`)
}

// corpusBodyWire splices raw wire bytes verbatim as the content value. The
// wire field contains the exact JSON text (string or array) that appeared in
// the transcript, obtained via gjson.Result.Raw. This is the wire-faithful
// path: no re-serialization, no reshaping.
func corpusBodyWire(wire, tool, command string) []byte {
	input := `{}`
	if command != "" {
		c, err := json.Marshal(command)
		if err != nil {
			return nil
		}
		input = `{"command":` + string(c) + `}`
	}
	name, err := json.Marshal(tool)
	if err != nil {
		return nil
	}
	return []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"go"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":` +
		string(name) + `,"input":` + input + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` +
		wire + `}]}]}`)
}

// controlBlocks are known-compressible corpora injected into the run. If these
// do not come back with expected reach/action, the harness is broken and every
// 0% it reports is meaningless — the failure mode that produced a vacuous
// 73,110-comparison measurement on this project once already.
func controlBlocks() []inBlock {
	var b strings.Builder
	b.WriteString("FAILED build with 1 error\n")
	for i := 0; i < 200; i++ {
		b.WriteString("2026-01-01 00:00:00 INFO  worker: processed batch id=000000 status=ok latency_ms=12\n")
	}
	log := b.String()
	logWire, _ := json.Marshal(log)

	var diff strings.Builder
	diff.WriteString("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,4 +1,4 @@\n")
	for i := 0; i < 200; i++ {
		diff.WriteString(" unchanged context line that carries no information at all\n")
	}
	diffWire, _ := json.Marshal(diff.String())

	var search strings.Builder
	for i := 0; i < 200; i++ {
		search.WriteString("internal/pkg/file.go:42:  matched the search term here\n")
	}
	searchWire, _ := json.Marshal(search.String())

	// Array-wrapped text: same log but in [{"type":"text","text":"..."}] format
	arrayTextWire := `[{"type":"text","text":` + string(logWire) + `}]`

	// Array-wrapped image: 40x40 PNG (must be >=512 bytes base64)
	imgData := make40x40PNGForControl()
	imgDataWire, _ := json.Marshal(imgData)
	arrayImageWire := `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":` + string(imgDataWire) + `}}]`

	// Unreachable element type
	arrayUnreachableWire := `[{"type":"tool_reference","tool_use_id":"x","field":"data"}]`
	// Pad to >= 512 bytes
	padding := ""
	for len(arrayUnreachableWire)+len(padding) < 512 {
		padding += " "
	}
	arrayUnreachableWire = `[{"type":"tool_reference","tool_use_id":"x","field":"data","pad":"` + padding + `"}]`

	// Empty array
	arrayEmptyWire := `[]`

	return []inBlock{
		{Tool: "?CONTROL-log", Cmd: "", Origin: "control", Sha: "ctl-log", Shape: "string",
			WireBytes: len(logWire), Wire: string(logWire), Occurrences: 1},
		{Tool: "?CONTROL-diff", Cmd: "", Origin: "control", Sha: "ctl-diff", Shape: "string",
			WireBytes: len(diffWire), Wire: string(diffWire), Occurrences: 1},
		{Tool: "?CONTROL-search", Cmd: "", Origin: "control", Sha: "ctl-search", Shape: "string",
			WireBytes: len(searchWire), Wire: string(searchWire), Occurrences: 1},
		{Tool: "Bash", Cmd: "make build", Origin: "control", Sha: "ctl-log-bash", Shape: "string",
			WireBytes: len(logWire), Wire: string(logWire), Occurrences: 1},
		{Tool: "Bash", Cmd: "", Origin: "control", Sha: "ctl-array-text", Shape: "array",
			WireBytes: len(arrayTextWire), Wire: arrayTextWire, Occurrences: 1},
		{Tool: "Bash", Cmd: "", Origin: "control", Sha: "ctl-array-image", Shape: "array",
			WireBytes: len(arrayImageWire), Wire: arrayImageWire, Occurrences: 1},
		{Tool: "Bash", Cmd: "", Origin: "control", Sha: "ctl-array-unreachable", Shape: "array",
			WireBytes: len(arrayUnreachableWire), Wire: arrayUnreachableWire, Occurrences: 1},
		{Tool: "Bash", Cmd: "", Origin: "control", Sha: "ctl-no-outcome", Shape: "array",
			WireBytes: len(arrayEmptyWire), Wire: arrayEmptyWire, Occurrences: 1},
		// Synthetic reshaped block
		{Tool: "Bash", Cmd: "", Origin: "control", Sha: "ctl-reshaped", Shape: "string",
			WireBytes: 1000, Wire: `"placeholder"`, Reshaped: "invalid_utf8", Occurrences: 1},
	}
}

func make40x40PNGForControl() string {
	// Generate a real PNG large enough to exceed 512 bytes base64
	// Use 100x100 with a pattern that doesn't compress well
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			// Use a pseudo-random pattern to prevent compression
			img.Set(x, y, color.RGBA{
				R: uint8((x*73 + y*157) % 256),
				G: uint8((x*97 + y*181) % 256),
				B: uint8((x*131 + y*211) % 256),
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return encoded
}

// reachStats accumulates metrics for the reach table
type reachStats struct {
	rows       int
	textTokens int
	// textTokensAfter is what the same rows cost AFTER the dispatcher acted.
	// Reach and saving are different questions and this table answers both:
	// a block can be reached, acted on, and still barely shrink.
	textTokensAfter int
	wireBytes       int
}

func TestCorpusClassify(t *testing.T) {
	in := envOr("TIO_IN", "/tmp/tio/blocks.jsonl")

	f, err := os.Open(in)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	rt := router.NewDefault()
	tok := mustTokenizer(t)

	// Accumulators for the reach table
	byReach := make(map[string]*reachStats)
	byAction := make(map[string]*reachStats)

	visualStats := &reachStats{}
	countedBlocks := make(map[string]bool) // Track which blocks we've counted for wire bytes

	run := func(b inBlock, isControl bool) {
		// Handle reshaped blocks
		if b.Reshaped != "" {
			if !isControl {
				if byReach["excluded_reshaped"] == nil {
					byReach["excluded_reshaped"] = &reachStats{}
				}
				byReach["excluded_reshaped"].rows++
				byReach["excluded_reshaped"].wireBytes += b.WireBytes
			}
			return
		}

		store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
		if err != nil {
			t.Fatal(err)
		}

		body := corpusBodyWire(b.Wire, b.Tool, b.Cmd)
		res := Dispatch(body, Options{
			Policy:      policy.ForMode(policy.PAYG),
			Router:      rt,
			Store:       store,
			FrozenCount: 0,
		})

		// THE GUARD: len(res.Blocks)==0 must record as no_outcome, never skip
		rows := rowsFor(res)
		if len(rows) == 0 {
			rows = []corpusRow{{Reach: "no_outcome", Reason: string(res.Reason)}}
		}

		// For corpus blocks, fill tokens and accumulate
		for i := range rows {
			row := &rows[i]
			row.WireBytes = b.WireBytes

			// Fill tokens based on reach/action
			if row.TokensBefore == 0 {
				switch row.Action {
				case "no_op", "below_threshold", "hot_zone", "store_unresolvable":
					// Count text tokens from wire
					row.TokensBefore = tok.CountText(b.Wire)
					row.TokenKind = "text"
				case "image_declined":
					// Count visual tokens from base64
					row.TokensBefore = visualTokensFromWireImage(b.Wire)
					row.TokenKind = "visual"
				case "unreachable", "":
					row.TokensBefore = tok.CountText(b.Wire)
					row.TokenKind = "text"
				}
			}

			// Accumulate per-row stats (occurrence-weighted)
			if !isControl {
				occ := b.Occurrences
				if occ == 0 {
					occ = 1
				}

				reach := row.Reach
				if byReach[reach] == nil {
					byReach[reach] = &reachStats{}
				}
				byReach[reach].rows += occ

				if row.TokenKind == "text" {
					byReach[reach].textTokens += row.TokensBefore * occ
					byReach[reach].textTokensAfter += row.TokensAfter * occ
				} else if row.TokenKind == "visual" {
					visualStats.rows += occ
					visualStats.textTokens += row.TokensBefore * occ
					visualStats.textTokensAfter += row.TokensAfter * occ
				}

				// Also accumulate by action for declined breakdown
				if reach == "declined" && row.Action != "" {
					if byAction[row.Action] == nil {
						byAction[row.Action] = &reachStats{}
					}
					byAction[row.Action].rows += occ
					byAction[row.Action].textTokens += row.TokensBefore * occ
				}
			}
		}

		// Wire bytes: attribute once per unique block to primary reach state
		// Do NOT multiply by occurrences here - we want unique bytes for the unique denominator
		if !isControl && !countedBlocks[b.Sha] && len(rows) > 0 {
			primaryReach := rows[0].Reach
			byReach[primaryReach].wireBytes += b.WireBytes

			// For visual blocks, also count in visualStats
			if rows[0].TokenKind == "visual" {
				visualStats.wireBytes += b.WireBytes
			}

			countedBlocks[b.Sha] = true
		}

		// For controls, validate expectations
		if isControl {
			validateControl(t, b, res, rows)
		}
	}

	// Run controls first
	for _, c := range controlBlocks() {
		run(c, true)
	}

	// Run corpus
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	n := 0
	for sc.Scan() {
		var b inBlock
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			continue
		}
		run(b, false)
		n++
		if n%5000 == 0 {
			t.Logf("processed %d blocks", n)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan corpus: %v", err)
	}

	// Read meta for denominators
	inPath := envOr("TIO_IN", "/tmp/tio/blocks.jsonl")
	metaPath := filepath.Join(filepath.Dir(inPath), "meta.json")
	var meta extractMeta
	if mf, err := os.Open(metaPath); err == nil {
		json.NewDecoder(mf).Decode(&meta)
		mf.Close()
	}

	// Print reach table
	printReachTable(t, byReach, byAction, visualStats, meta, n)
}

func mustTokenizer(t *testing.T) tokenizer.Tokenizer {
	// Use claude-sonnet-5 to get the estimator (default for Anthropic models)
	return tokenizer.GetTokenizer("claude-sonnet-5")
}

func visualTokensFromWireImage(wire string) int {
	// Parse the wire to extract base64 data from image block(s)
	// Wire can be string or array format
	if len(wire) == 0 {
		return 0
	}

	totalTokens := 0
	if wire[0] == '[' {
		// Array format: may contain multiple images
		parsed := gjson.Parse(wire)
		if parsed.IsArray() {
			for _, elem := range parsed.Array() {
				if elem.Get("type").String() == "image" {
					base64Data := elem.Get("source.data").String()
					if base64Data != "" {
						totalTokens += visualTokensFromBase64(base64Data)
					}
				}
			}
		}
	} else {
		// String format: just the base64 directly
		base64Data := strings.Trim(wire, `"`)
		if base64Data != "" {
			totalTokens = visualTokensFromBase64(base64Data)
		}
	}

	return totalTokens
}

func validateControl(t *testing.T, b inBlock, res Result, rows []corpusRow) {
	// Validate control expectations per the spec
	switch b.Sha {
	case "ctl-log":
		expectControl(t, b.Sha, rows, "acted", "compressed", -1)
	case "ctl-diff":
		expectControl(t, b.Sha, rows, "acted", "compressed", -1)
	case "ctl-search":
		// F8: the comment was wrong - SearchOffload is deliberately unregistered
		expectControl(t, b.Sha, rows, "declined", "no_op", -1)
	case "ctl-log-bash":
		expectControl(t, b.Sha, rows, "acted", "compressed", -1)
	case "ctl-array-text":
		expectControl(t, b.Sha, rows, "acted", "compressed", 0)
		if len(rows) > 0 && rows[0].ElemType != "text" {
			t.Fatalf("%s: elem_type=%q, want text", b.Sha, rows[0].ElemType)
		}
	case "ctl-array-image":
		expectControl(t, b.Sha, rows, "declined", "image_declined", 0)
		if len(rows) > 0 && rows[0].TokenKind != "visual" {
			t.Fatalf("%s: token_kind=%q, want visual", b.Sha, rows[0].TokenKind)
		}
	case "ctl-array-unreachable":
		expectControl(t, b.Sha, rows, "unreachable", "unreachable", 0)
		// Assert that Reason is no_candidates while reach is unreachable (F2)
		if res.Reason != "no_candidates" {
			t.Fatalf("%s: Reason=%q, want no_candidates (pinning the conflation)", b.Sha, res.Reason)
		}
	case "ctl-no-outcome":
		if len(res.Blocks) != 0 {
			t.Fatalf("%s: len(Blocks)=%d, want 0", b.Sha, len(res.Blocks))
		}
		if len(rows) == 0 || rows[0].Reach != "no_outcome" {
			t.Fatalf("%s: reach=%q, want no_outcome", b.Sha, rows[0].Reach)
		}
	case "ctl-reshaped":
		// Already handled in run() - just verify it didn't get classified
		if len(rows) > 0 {
			t.Fatalf("%s: reshaped block produced rows", b.Sha)
		}
	}
}

func expectControl(t *testing.T, sha string, rows []corpusRow, wantReach, wantAction string, wantCI int) {
	if len(rows) == 0 {
		t.Fatalf("%s: no rows", sha)
	}
	r := rows[0]
	if r.Reach != wantReach {
		t.Fatalf("%s: reach=%q, want %q", sha, r.Reach, wantReach)
	}
	if r.Action != wantAction {
		t.Fatalf("%s: action=%q, want %q", sha, r.Action, wantAction)
	}
	if r.ContentIndex != wantCI {
		t.Fatalf("%s: content_index=%d, want %d", sha, r.ContentIndex, wantCI)
	}
}

func printReachTable(t *testing.T, byReach map[string]*reachStats, byAction map[string]*reachStats, visualStats *reachStats, meta extractMeta, corpusBlocks int) {
	// Print reach table with tokens leading, bytes secondary

	allTextTokens := 0
	for reach, s := range byReach {
		if reach != "excluded_reshaped" {
			allTextTokens += s.textTokens
		}
	}

	reachableTextTokens := 0
	if byReach["acted"] != nil {
		reachableTextTokens += byReach["acted"].textTokens
	}
	if byReach["declined"] != nil {
		reachableTextTokens += byReach["declined"].textTokens
	}

	t.Logf("\n=== CORPUS REACH — TEXT TOKENS (primary currency) ===")
	t.Logf("Denominators:")
	t.Logf("  ALL = %d unique blocks / %d text tokens / %.1f MB wire",
		meta.UniqueBlocks, allTextTokens, float64(meta.UniqueWireBytes)/1e6)
	t.Logf("  (as sent, with re-sends: %d blocks / %.1f MB)",
		meta.BlocksSeen, float64(meta.WireBytesSeen)/1e6)
	t.Logf("  REACHABLE = acted + declined only")
	t.Logf("")
	t.Logf("  READ THE WEIGHTING. The two halves of this table count different")
	t.Logf("  populations, on purpose, and comparing them is a category error:")
	t.Logf("    token columns are OCCURRENCE-WEIGHTED (as-sent) - a block re-sent")
	t.Logf("      74 times cost the window 74 times, which is what a token asks")
	t.Logf("    byte columns are UNIQUE-weighted - each distinct block counted once,")
	t.Logf("      which is what a corpus-composition question asks")
	t.Logf("  So a row's token %% and its byte %% are not two views of one number.")
	t.Logf("")

	printReachRow := func(label string, s *reachStats, denom int) {
		if s == nil {
			s = &reachStats{}
		}
		pctAll := 0.0
		if allTextTokens > 0 {
			pctAll = 100.0 * float64(s.textTokens) / float64(allTextTokens)
		}
		pctReach := 0.0
		if denom > 0 {
			pctReach = 100.0 * float64(s.textTokens) / float64(denom)
		}

		pctAllBytes := 0.0
		if meta.UniqueWireBytes > 0 {
			pctAllBytes = 100.0 * float64(s.wireBytes) / float64(meta.UniqueWireBytes)
		}

		reachStr := "—"
		if denom > 0 {
			reachStr = fmt.Sprintf("%.1f%%", pctReach)
		}

		t.Logf("%-20s %6d %12d %8.1f%% %12s %12d %8.1f%%",
			label, s.rows, s.textTokens, pctAll, reachStr, s.wireBytes, pctAllBytes)
	}

	t.Logf("%-20s %6s %12s %8s %12s %12s %8s",
		"reach", "rows", "tok as-sent", "% tok", "% tok REACH", "bytes uniq", "% bytes")
	t.Logf("%-20s %6s %12s %8s %12s %12s %8s", "----", "----", "----", "----", "----", "----", "----")

	printReachRow("acted", byReach["acted"], reachableTextTokens)
	printReachRow("declined", byReach["declined"], reachableTextTokens)
	printReachRow("unreachable", byReach["unreachable"], 0)
	printReachRow("no_outcome", byReach["no_outcome"], 0)
	printReachRow("excluded_reshaped", byReach["excluded_reshaped"], 0)

	t.Logf("")
	t.Logf("=== DECLINED BREAKDOWN ===")
	// Sorted, because an unsorted map range makes two runs of the same corpus
	// diff against each other for no reason, and this table exists to be
	// compared between runs.
	declinedActions := make([]string, 0, len(byAction))
	for action := range byAction {
		declinedActions = append(declinedActions, action)
	}
	sort.Slice(declinedActions, func(i, j int) bool {
		a, b := byAction[declinedActions[i]], byAction[declinedActions[j]]
		if a.textTokens != b.textTokens {
			return a.textTokens > b.textTokens
		}
		return declinedActions[i] < declinedActions[j]
	})
	for _, action := range declinedActions {
		printReachRow("  "+action, byAction[action], 0)
	}

	// REACH IS NOT SAVING, and the historical figure this table replaces was a
	// saving. Without this section "acted 45.5%" reads as "45.5% saved", which
	// is the same family of error as calling a byte share what a user sees.
	t.Logf("")
	t.Logf("=== WHAT WAS ACTUALLY REMOVED — the saving, not the reach ===")
	if acted := byReach["acted"]; acted != nil {
		removed := acted.textTokens - acted.textTokensAfter
		t.Logf("  acted rows        %d tok before -> %d after, %d removed",
			acted.textTokens, acted.textTokensAfter, removed)
		if acted.textTokens > 0 {
			t.Logf("  cut within acted  %.1f%%   of the tokens the dispatcher acted on",
				100.0*float64(removed)/float64(acted.textTokens))
		}
		if allTextTokens > 0 {
			t.Logf("  cut over ALL      %.1f%%   of every text token in the corpus, as-sent",
				100.0*float64(removed)/float64(allTextTokens))
			t.Logf("                    ^ THIS is the figure comparable to the old 38.3%%,")
			t.Logf("                      which was quoted over a denominator that included")
			t.Logf("                      bytes production could not then reach.")
		}
	}

	t.Logf("")
	t.Logf("=== IMAGES — VISUAL TOKENS (never summed with text) ===")
	printReachRow("visual", visualStats, 0)
	pctImageBytes := 0.0
	if meta.UniqueWireBytes > 0 {
		pctImageBytes = 100.0 * float64(visualStats.wireBytes) / float64(meta.UniqueWireBytes)
	}
	t.Logf("CAUTION: images are %.1f%% of unique tool_result WIRE BYTES,", pctImageBytes)
	t.Logf("         but %d visual tokens (not text tokens).", visualStats.textTokens)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// withSearchOffload is NewDefault plus the one transform the production router
// deliberately leaves out. hr-tio needs the counterfactual measured, not
// assumed.
func withSearchOffload() *router.Router {
	return router.New(pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithReformat(reformats.LogTemplate{}).
		WithOffload(offloads.NewDiffNoise()).
		WithOffload(offloads.NewDiffOffload(compress.NewDiffCompressor())).
		WithOffload(offloads.NewJsonOffloadWith(smartcrusher.NewSmartCrusher(smartcrusher.DefaultConfig()))).
		WithOffload(offloads.NewLogOffload(compress.NewLogCompressor())).
		WithOffload(offloads.NewSearchOffload(compress.NewSearchCompressor())).
		Build())
}

// TestCorpusSearchCounterfactual re-runs the corpus through both routers and
// reports what registering SearchOffload would change.
func TestCorpusSearchCounterfactual(t *testing.T) {
	f, err := os.Open(envOr("TIO_IN", "/tmp/tio/blocks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	base, plus := router.NewDefault(), withSearchOffload()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)

	var (
		blocks                      int
		baseHits, plusHits          int
		baseBytesIn, baseBytesSaved int
		plusBytesIn, plusBytesSaved int
		newlyCompressed             int
		newlyIn, newlyOut           int
	)
	run := func(rt *router.Router, wire, tool, cmd string) (bool, int) {
		store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
		res := Dispatch(corpusBodyWire(wire, tool, cmd), Options{
			Policy: policy.ForMode(policy.PAYG), Router: rt, Store: store, FrozenCount: 0})
		for _, b := range res.Blocks {
			if (b.BlockType == "tool_result" || b.ContentIndex >= 0) && b.Action == "compressed" {
				return true, b.TokensAfter
			}
		}
		return false, 0
	}

	for sc.Scan() {
		var b inBlock
		if json.Unmarshal(sc.Bytes(), &b) != nil {
			continue
		}
		blocks++
		okBase, _ := run(base, b.Wire, b.Tool, b.Cmd)
		okPlus, _ := run(plus, b.Wire, b.Tool, b.Cmd)
		if okBase {
			baseHits++
			baseBytesIn += b.WireBytes
		}
		if okPlus {
			plusHits++
			plusBytesIn += b.WireBytes
		}
		if !okBase && okPlus {
			newlyCompressed++
			newlyIn += b.WireBytes
			// Measure the actual emitted size for the newly-covered blocks.
			// Note: Router.Compress expects the extracted text, not wire JSON
			var text string
			if len(b.Wire) > 0 && b.Wire[0] == '"' {
				// String wire: unquote
				json.Unmarshal([]byte(b.Wire), &text)
			} else {
				// Array wire: extract text from first element if present
				elem := gjson.Get(b.Wire, "0.text")
				if elem.Exists() {
					text = elem.String()
				}
			}
			if text != "" {
				store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
				r := plus.Compress(text, transform.CompressionContext{}, store)
				newlyOut += len(r.Output)
			}
		}
	}
	_ = baseBytesSaved
	_ = plusBytesSaved

	rep := fmt.Sprintf(
		"corpus blocks                 : %d\n"+
			"compressed, default router    : %d  (%.1f MB of input)\n"+
			"compressed, +search_offload   : %d  (%.1f MB of input)\n"+
			"NEWLY covered by search       : %d blocks, %.1f MB -> %.1f MB (%.1f%% cut)\n",
		blocks, baseHits, float64(baseBytesIn)/1e6, plusHits, float64(plusBytesIn)/1e6,
		newlyCompressed, float64(newlyIn)/1e6, float64(newlyOut)/1e6,
		100*(1-float64(newlyOut)/float64(max(1, newlyIn))))
	if err := os.WriteFile("/tmp/tio/counterfactual.txt", []byte(rep), 0o644); err != nil {
		t.Fatal(err)
	}
}
