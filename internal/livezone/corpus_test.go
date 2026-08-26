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
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

type inBlock struct {
	Tool string `json:"tool"`
	// Cmd is the shell command for a Bash-family tool. The read-protection
	// gate needs it: `cat foo.rb` is a file read even though the tool is Bash.
	Cmd    string `json:"cmd"`
	Origin string `json:"origin"`
	Sha    string `json:"sha"`
	Len    int    `json:"len"`
	Text   string `json:"text"`
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

// controlBlocks are known-compressible corpora injected into the run. If these
// do not come back compressed, the harness is broken and every 0% it reports is
// meaningless — the failure mode that produced a vacuous 73,110-comparison
// measurement on this project once already.
func controlBlocks() []inBlock {
	var b strings.Builder
	b.WriteString("FAILED build with 1 error\n")
	for i := 0; i < 200; i++ {
		b.WriteString("2026-01-01 00:00:00 INFO  worker: processed batch id=000000 status=ok latency_ms=12\n")
	}
	log := b.String()

	var diff strings.Builder
	diff.WriteString("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,4 +1,4 @@\n")
	for i := 0; i < 200; i++ {
		diff.WriteString(" unchanged context line that carries no information at all\n")
	}

	var search strings.Builder
	for i := 0; i < 200; i++ {
		search.WriteString("internal/pkg/file.go:42:  matched the search term here\n")
	}

	return []inBlock{
		{Tool: "?CONTROL-log", Origin: "control", Sha: "ctl-log", Len: len(log), Text: log},
		{Tool: "?CONTROL-diff", Origin: "control", Sha: "ctl-diff", Len: diff.Len(), Text: diff.String()},
		{Tool: "?CONTROL-search", Origin: "control", Sha: "ctl-search", Len: search.Len(), Text: search.String()},
	}
}

func TestCorpusClassify(t *testing.T) {
	in := envOr("TIO_IN", "/tmp/tio/blocks.jsonl")
	outPath := envOr("TIO_OUT", "/tmp/tio/results.jsonl")

	f, err := os.Open(in)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	of, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	defer of.Close()
	w := bufio.NewWriterSize(of, 1<<20)
	defer w.Flush()

	rt := router.NewDefault()
	enc := json.NewEncoder(w)

	run := func(b inBlock) {
		store, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
		if err != nil {
			t.Fatal(err)
		}
		res := Dispatch(corpusBodyFor(b.Text, b.Tool, b.Cmd), Options{
			Policy:      policy.ForMode(policy.PAYG),
			Router:      rt,
			Store:       store,
			FrozenCount: 0,
		})
		o := outBlock{
			Tool: b.Tool, Origin: b.Origin, Sha: b.Sha, Len: b.Len,
			Detected: rt.Detect(b.Text).Type.String(),
			Reason:   string(res.Reason),
		}
		for _, blk := range res.Blocks {
			// Nested blocks have BlockType "text"/"image" and ContentIndex >= 0
			if blk.BlockType == "tool_result" || blk.ContentIndex >= 0 {
				o.Action, o.Strategy = blk.Action, blk.Strategy
				o.Before, o.After = blk.TokensBefore, blk.TokensAfter
			}
		}
		_ = enc.Encode(o)
	}

	for _, c := range controlBlocks() {
		run(c)
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	n := 0
	for sc.Scan() {
		var b inBlock
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			continue
		}
		run(b)
		n++
		if n%5000 == 0 {
			t.Logf("processed %d blocks", n)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan corpus: %v", err)
	}
	t.Logf("processed %d corpus blocks + %d controls", n, len(controlBlocks()))
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
		okBase, _ := run(base, b.Text)
		okPlus, _ := run(plus, b.Text)
		if okBase {
			baseHits++
			baseBytesIn += b.Len
		}
		if okPlus {
			plusHits++
			plusBytesIn += b.Len
		}
		if !okBase && okPlus {
			newlyCompressed++
			newlyIn += b.Len
			// Measure the actual emitted size for the newly-covered blocks.
			store, _ := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 8})
			r := plus.Compress(b.Text, transform.CompressionContext{}, store)
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
