package livezone

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// Detection is only half the story: the point of classifying verbose Go test
// output as BuildOutput is that the router then reaches the log compressor.
// This drives the WHOLE pipeline on the real captured block and asserts a
// real saving, so a future detector change that quietly re-routes it fails
// here rather than showing up months later as 0% on someone's machine.
func TestVerboseGoTestOutputActuallyCompresses(t *testing.T) {
	raw, err := os.ReadFile("../detect/testdata/go_test_verbose_failing.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	enc, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"claude-sonnet-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"run the tests"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + string(enc) + `}]}]}`)

	store := newMapStore()
	res := Dispatch(body, Options{
		Router:      router.NewDefault(),
		Store:       store,
		Tokenizer:   tokenizer.GetTokenizer("claude"),
		FrozenCount: 0,
	})

	if !res.Applied {
		t.Fatalf("a %d-byte real `go test -v` block was not compressed: reason=%q", len(text), res.Reason)
	}

	saved := float64(len(body)-len(res.Body)) / float64(len(body))
	t.Logf("body %d -> %d bytes (%.1f%% of the whole body), tokens %d -> %d",
		len(body), len(res.Body), 100*saved, res.TokensBefore, res.TokensAfter)

	// The measured counterfactual was 75.5%. Assert a floor well under it so
	// the test pins the behaviour rather than the exact ratio.
	if saved < 0.50 {
		t.Errorf("saved only %.1f%% of the body; the log compressor gave 75.5%% on this block", 100*saved)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Errorf("tokens %d -> %d: the I5 gate should not have accepted this", res.TokensBefore, res.TokensAfter)
	}

	// Every marker it put on the wire must resolve, or the saving is a lie.
	out := string(res.Body)
	if !strings.Contains(out, "<<ccr:") {
		t.Error("no canonical marker in the output")
	}
	for _, b := range res.Blocks {
		if b.Action == "compressed" && b.CacheKey != "" {
			if got, ok := store.Get(b.CacheKey); !ok || got != text {
				t.Errorf("marker %s does not resolve to the original", b.CacheKey)
			}
		}
	}
}
