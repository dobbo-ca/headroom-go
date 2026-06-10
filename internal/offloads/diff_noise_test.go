package offloads

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func store(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiffNoiseDropsLockfileHunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n")
	for i := 0; i < 40; i++ {
		b.WriteString("@@ -1 +1 @@\n+example.com/x v1.0.0 h1:abc\n")
	}
	in := b.String()
	st := store(t)
	dn := NewDiffNoise()
	if dn.EstimateBloat(in) <= 0 {
		t.Fatal("lockfile diff should look bloated")
	}
	out, err := dn.Apply(in, transform.CompressionContext{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "[diff_noise: lockfile hunks dropped") {
		t.Fatal("expected lockfile cell marker")
	}
	if !strings.Contains(out.Output, "[diff_noise CCR: hash=") {
		t.Fatal("expected trailing CCR marker")
	}
	if v, ok := st.Get(out.CacheKey); !ok || v != in {
		t.Fatal("original retrievable under CacheKey")
	}
	if out.CacheKey != ccr.ComputeKeyMD5([]byte(in)) {
		t.Fatalf("CacheKey should be md5_hex_24(content): got %q", out.CacheKey)
	}
}

func TestDiffNoiseDropsWhitespaceOnlyHunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go\n")
	for i := 0; i < 40; i++ {
		// Same body text after ASCII-ws strip on both - and + sides.
		b.WriteString("@@ -1 +1 @@\n-foo bar\n+foo  bar\n")
	}
	in := b.String()
	st := store(t)
	dn := NewDiffNoise()
	out, err := dn.Apply(in, transform.CompressionContext{}, st)
	if err != nil {
		t.Fatalf("whitespace-only diff should offload: %v", err)
	}
	if !strings.Contains(out.Output, "[diff_noise: whitespace-only hunks dropped") {
		t.Fatalf("expected whitespace-only marker: %q", out.Output)
	}
}

func TestDiffNoisePureContextHunkNotWhitespaceOnly(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/src/main.go b/src/main.go\n--- a/src/main.go\n+++ b/src/main.go\n")
	for i := 0; i < 40; i++ {
		b.WriteString("@@ -1 +1 @@\n context only line\n")
	}
	in := b.String()
	st := store(t)
	dn := NewDiffNoise()
	_, err := dn.Apply(in, transform.CompressionContext{}, st)
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("pure-context hunks are not droppable -> ErrSkipped, got %v", err)
	}
}

func TestIsLockfilePathBoundary(t *testing.T) {
	// MyCargo.lock must NOT match; crates/foo/Cargo.lock and bare Cargo.lock must.
	cases := []struct {
		path string
		want bool
	}{
		{"Cargo.lock", true},
		{"crates/foo/Cargo.lock", true},
		{"MyCargo.lock", false},
		{"FakeCargo.lockfile", false},
		{"go.sum", true},
		{"vendor/go.sum", true},
		{"", false},
		{"a\\b\\package-lock.json", true},
	}
	dn := NewDiffNoise()
	for _, c := range cases {
		if got := dn.isLockfile(c.path); got != c.want {
			t.Errorf("isLockfile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestDiffNoiseEmptyBloatZero(t *testing.T) {
	if got := NewDiffNoise().EstimateBloat(""); got != 0 {
		t.Fatalf("EstimateBloat(\"\") = %v, want 0", got)
	}
}

func TestDiffNoiseNoSectionsSkipped(t *testing.T) {
	// 30+ lines but no "diff --git" sections.
	in := strings.Repeat("just a plain line\n", 40)
	_, err := NewDiffNoise().Apply(in, transform.CompressionContext{}, store(t))
	if !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("no diff sections -> ErrSkipped, got %v", err)
	}
}

var _ transform.OffloadTransform = (*DiffNoise)(nil)
