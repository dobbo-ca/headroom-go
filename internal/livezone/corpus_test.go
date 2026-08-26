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
	"encoding/json"
	"fmt"
	"os"
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
	// Minimal PNG that encodes to >512 bytes base64
	var buf []byte
	buf = append(buf, 0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
	// IHDR
	ihdr := []byte{
		0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x28,
		0x00, 0x00, 0x00, 0x28,
		0x08, 0x02, 0x00, 0x00, 0x00,
		0x4E, 0xEC, 0x6C, 0x2E,
	}
	buf = append(buf, ihdr...)
	// IDAT with enough data to exceed 512 base64
	idatData := make([]byte, 400)
	for i := range idatData {
		idatData[i] = byte(i % 256)
	}
	idat := []byte{0x00, 0x00, 0x01, 0x90, 0x49, 0x44, 0x41, 0x54}
	idat = append(idat, idatData...)
	idat = append(idat, 0x00, 0x00, 0x00, 0x00)
	buf = append(buf, idat...)
	// IEND
	iend := []byte{0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
	buf = append(buf, iend...)

	// Base64 encode
	const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result []byte
	for i := 0; i < len(buf); i += 3 {
		chunk := uint32(buf[i]) << 16
		if i+1 < len(buf) {
			chunk |= uint32(buf[i+1]) << 8
		}
		if i+2 < len(buf) {
			chunk |= uint32(buf[i+2])
		}
		result = append(result, base64Chars[(chunk>>18)&0x3F])
		result = append(result, base64Chars[(chunk>>12)&0x3F])
		if i+1 < len(buf) {
			result = append(result, base64Chars[(chunk>>6)&0x3F])
		} else {
			result = append(result, '=')
		}
		if i+2 < len(buf) {
			result = append(result, base64Chars[chunk&0x3F])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}

// reachStats accumulates metrics for the reach table
type reachStats struct {
	rows       int
	textTokens int
	wireBytes  int
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

			// Accumulate stats (occurrence-weighted)
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
				byReach[reach].wireBytes += b.WireBytes * occ

				if row.TokenKind == "text" {
					byReach[reach].textTokens += row.TokensBefore * occ
				} else if row.TokenKind == "visual" {
					visualStats.rows += occ
					visualStats.textTokens += row.TokensBefore * occ
					visualStats.wireBytes += b.WireBytes * occ
				}

				// Also accumulate by action for declined breakdown
				if reach == "declined" && row.Action != "" {
					if byAction[row.Action] == nil {
						byAction[row.Action] = &reachStats{}
					}
					byAction[row.Action].rows += occ
					byAction[row.Action].textTokens += row.TokensBefore * occ
					byAction[row.Action].wireBytes += b.WireBytes * occ
				}
			}
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
	metaPath := envOr("TIO_IN", "/tmp/tio") + "/../meta.json"
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
	// Parse the wire to extract base64 data from image block
	// Wire can be string or array format
	if len(wire) == 0 {
		return 0
	}

	var base64Data string
	if wire[0] == '[' {
		// Array format: [{"type":"image","source":{"data":"..."}}]
		result := gjson.Get(wire, "0.source.data")
		base64Data = result.String()
	} else {
		// String format: just the base64 directly
		base64Data = strings.Trim(wire, `"`)
	}

	if base64Data == "" {
		return 0
	}

	return visualTokensFromBase64(base64Data)
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

	t.Logf("%-20s %6s %12s %8s %12s %12s %8s", "reach", "rows", "text tokens", "% ALL", "% REACHABLE", "wire bytes", "% ALL bytes")
	t.Logf("%-20s %6s %12s %8s %12s %12s %8s", "----", "----", "----", "----", "----", "----", "----")

	printReachRow("acted", byReach["acted"], reachableTextTokens)
	printReachRow("declined", byReach["declined"], reachableTextTokens)
	printReachRow("unreachable", byReach["unreachable"], 0)
	printReachRow("no_outcome", byReach["no_outcome"], 0)
	printReachRow("excluded_reshaped", byReach["excluded_reshaped"], 0)

	t.Logf("")
	t.Logf("=== DECLINED BREAKDOWN ===")
	for action, s := range byAction {
		printReachRow("  "+action, s, 0)
	}

	t.Logf("")
	t.Logf("=== IMAGES — VISUAL TOKENS (never summed with text) ===")
	printReachRow("visual", visualStats, 0)
	t.Logf("CAUTION: images are %.1f%% of unique tool_result WIRE BYTES but",
		100.0*float64(visualStats.wireBytes)/float64(meta.UniqueWireBytes))
	t.Logf("         %d visual tokens. Counting that base64 as text would overstate by ~57x.",
		visualStats.textTokens)
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
	run := func(rt *router.Router, text string) (bool, int) {
		store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
		res := Dispatch(corpusBody(text), Options{
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
		// Wire can be string or array - extract text for compression test
		text := b.Wire
		if len(text) > 0 && text[0] == '"' {
			// Unquote string wire
			var s string
			if json.Unmarshal([]byte(text), &s) == nil {
				text = s
			}
		}
		okBase, _ := run(base, text)
		okPlus, _ := run(plus, text)
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
			store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
			r := plus.Compress(text, transform.CompressionContext{}, store)
			newlyOut += len(r.Output)
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
