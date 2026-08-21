package offloads_test

// End-to-end coverage for the JsonOffload backed by the real Plan-3 SmartCrusher.
// This is an EXTERNAL test package (offloads_test) on purpose: smartcrusher imports
// offloads for the seam type (offloads.CrushResult), so an internal-package test
// importing smartcrusher would form a compile-time import cycle. An external test
// package is compiled separately and may close that loop, letting the test import
// both offloads and smartcrusher and exercise the injected crusher via
// offloads.NewJsonOffloadWith (the same constructor router.NewDefault uses).

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

// wrapperRe matches the json_offload trailing marker "\n[json_offload CCR:
// hash=<24hex>]". The wrapper key is ComputeKeyMD5(content) (24 hex), distinct
// from any inner smartcrusher CCR hash.
var wrapperRe = regexp.MustCompile(`\n\[json_offload CCR: hash=([0-9a-f]{24})\]$`)

// newSmartJsonOffload builds a JsonOffload wired to the real SmartCrusher, exactly
// as router.NewDefault does.
func newSmartJsonOffload() *offloads.JsonOffload {
	return offloads.NewJsonOffloadWith(smartcrusher.NewSmartCrusher(smartcrusher.DefaultConfig()))
}

func newStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// tabularArray builds a uniform tabular JSON array of n rows the SmartCrusher's
// lossless compaction table can crush.
func tabularArray(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"level":"info","msg":"ok"}`, i)
	}
	b.WriteString("]")
	return b.String()
}

// TestJsonOffloadSmartCrusherCompresses (case a): a large uniform tabular array
// compresses end-to-end. Output is shorter, BytesSaved>0, ends with the
// "\n[json_offload CCR: hash=<24hex>]" wrapper, and store.Get(CacheKey) returns
// the ORIGINAL content byte-exact (the wrapper key is ComputeKeyMD5(content),
// distinct from any inner smartcrusher hash).
func TestJsonOffloadSmartCrusherCompresses(t *testing.T) {
	st := newStore(t)
	content := tabularArray(50)

	out, err := newSmartJsonOffload().Apply(content, transform.CompressionContext{}, st)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(out.Output) >= len(content) {
		t.Fatalf("output not shorter: len(out)=%d len(in)=%d", len(out.Output), len(content))
	}
	if out.BytesSaved <= 0 {
		t.Fatalf("BytesSaved = %d, want > 0", out.BytesSaved)
	}
	m := wrapperRe.FindStringSubmatch(out.Output)
	if m == nil {
		t.Fatalf("output does not end with json_offload wrapper marker: %q", out.Output)
	}
	wrapperHash := m[1]
	if out.CacheKey != wrapperHash {
		t.Fatalf("CacheKey %q != wrapper hash %q", out.CacheKey, wrapperHash)
	}
	if want := ccr.ComputeKeyMD5([]byte(content)); out.CacheKey != want {
		t.Fatalf("CacheKey %q != ComputeKeyMD5(content) %q", out.CacheKey, want)
	}
	got, ok := st.Get(out.CacheKey)
	if !ok {
		t.Fatalf("store.Get(%q) missing", out.CacheKey)
	}
	if got != content {
		t.Fatalf("store.Get returned modified original:\n got=%q\nwant=%q", got, content)
	}
}

// TestJsonOffloadSmartCrusherShortArraySkips (case b): a too-short array [1,2,3]
// yields no savings (SmartCrusher whitespace-normalizes but len(Compressed) >=
// len(content)), so Apply returns ErrSkipped and stores nothing.
func TestJsonOffloadSmartCrusherShortArraySkips(t *testing.T) {
	st := newStore(t)

	_, err := newSmartJsonOffload().Apply("[1,2,3]", transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("short array -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

// TestJsonOffloadSmartCrusherEmptyArraySkips (case c): [] is passthrough
// (WasModified=false) so Apply returns ErrSkipped, nothing stored.
func TestJsonOffloadSmartCrusherEmptyArraySkips(t *testing.T) {
	st := newStore(t)

	_, err := newSmartJsonOffload().Apply("[]", transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("empty array -> ErrSkipped, got %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("nothing should be stored on skip, store.Len()=%d", st.Len())
	}
}

// TestJsonOffloadSmartCrusherAnchorSurvives (case d): a row matching ctx.Query
// survives the crush end-to-end (query-anchor preservation flows from
// CompressionContext.Query through Crush).
func TestJsonOffloadSmartCrusherAnchorSurvives(t *testing.T) {
	st := newStore(t)

	const anchor = "correlation-99887766"
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		if i == 7 {
			fmt.Fprintf(&b, `{"id":%d,"status":"error","msg":"%s failed hard"}`, i, anchor)
		} else {
			fmt.Fprintf(&b, `{"id":%d,"status":"ok","msg":"row number %d ok"}`, i, i)
		}
	}
	b.WriteString("]")
	content := b.String()

	out, err := newSmartJsonOffload().Apply(content, transform.CompressionContext{Query: anchor}, st)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !strings.Contains(out.Output, anchor) {
		t.Fatalf("anchor row did not survive crush: output=%q", out.Output)
	}
}

// TestJsonOffloadSmartCrusherConstructsWithoutPanic proves the SmartCrusher-backed
// JsonOffload constructs (NewSmartCrusher builds an in-memory CCR store; the
// backend constructor is registered via smartcrusher's transitive blank import of
// internal/ccr/backends).
func TestJsonOffloadSmartCrusherConstructsWithoutPanic(t *testing.T) {
	if o := newSmartJsonOffload(); o == nil {
		t.Fatal("newSmartJsonOffload returned nil")
	}
}
