package livezone

import (
	"bytes"
	"testing"
)

// I4: the same (body, frozenCount, authMode) must produce byte-equal output
// every time. Any timestamp, random seed, or map-iteration order on the
// output path breaks this.
func TestDispatchIsDeterministic(t *testing.T) {
	in := bodyWith(compressibleLog())

	first := Dispatch(in, liveOptions(t))
	if !first.Applied {
		t.Fatalf("expected compression, got %q", first.Reason)
	}

	for i := 0; i < 25; i++ {
		// A fresh store each run: a store that leaked state into the output
		// would show up here as a divergence.
		got := Dispatch(in, liveOptions(t))
		if !bytes.Equal(got.Body, first.Body) {
			t.Fatalf("run %d produced different bytes\nfirst: %q\ngot:   %q", i, first.Body, got.Body)
		}
		if len(got.Rewritten) != len(first.Rewritten) {
			t.Fatalf("run %d rewrote %d ranges, first rewrote %d", i, len(got.Rewritten), len(first.Rewritten))
		}
		for j := range got.Rewritten {
			if got.Rewritten[j] != first.Rewritten[j] {
				t.Fatalf("run %d range %d = %+v, first = %+v", i, j, got.Rewritten[j], first.Rewritten[j])
			}
		}
		if got.TokensBefore != first.TokensBefore || got.TokensAfter != first.TokensAfter {
			t.Fatalf("run %d token counts diverged", i)
		}
	}
}

// Reusing one store across runs must not change the output either.
func TestDispatchIsDeterministicWithASharedStore(t *testing.T) {
	in := bodyWith(compressibleLog())
	opts := liveOptions(t)

	first := Dispatch(in, opts)
	if !first.Applied {
		t.Fatalf("expected compression, got %q", first.Reason)
	}
	for i := 0; i < 25; i++ {
		if got := Dispatch(in, opts); !bytes.Equal(got.Body, first.Body) {
			t.Fatalf("run %d with a shared store diverged", i)
		}
	}
}

// Determinism must hold across the whole I1 corpus, compressed or not.
func TestDispatchDeterministicAcrossCorpus(t *testing.T) {
	for name, body := range roundTripCorpus() {
		t.Run(name, func(t *testing.T) {
			in := []byte(body)
			first := Dispatch(in, liveOptions(t))
			for i := 0; i < 10; i++ {
				got := Dispatch(in, liveOptions(t))
				if !bytes.Equal(got.Body, first.Body) {
					t.Fatalf("run %d diverged", i)
				}
				if got.Reason != first.Reason {
					t.Fatalf("run %d reason = %q, first = %q", i, got.Reason, first.Reason)
				}
			}
		})
	}
}

// I4 with more than one rewrite per body. A single-rewrite body cannot
// observe iteration order at all, so this is the case that actually fails
// when a map reaches the output path.
func TestDispatchIsDeterministicWithMultipleBlocks(t *testing.T) {
	in := bodyTwoBlocks()

	first := Dispatch(in, liveOptions(t))
	if !first.Applied || len(first.Rewritten) != 2 {
		t.Fatalf("expected two rewrites, got applied=%v reason=%q ranges=%+v",
			first.Applied, first.Reason, first.Rewritten)
	}

	for i := 0; i < 25; i++ {
		got := Dispatch(in, liveOptions(t))
		if !bytes.Equal(got.Body, first.Body) {
			t.Fatalf("run %d produced different bytes", i)
		}
		if len(got.Rewritten) != len(first.Rewritten) {
			t.Fatalf("run %d rewrote %d ranges, first rewrote %d", i, len(got.Rewritten), len(first.Rewritten))
		}
		for j := range got.Rewritten {
			if got.Rewritten[j] != first.Rewritten[j] {
				t.Fatalf("run %d range %d = %+v, first = %+v", i, j, got.Rewritten[j], first.Rewritten[j])
			}
		}
		if len(got.Blocks) != len(first.Blocks) {
			t.Fatalf("run %d reported %d blocks, first reported %d", i, len(got.Blocks), len(first.Blocks))
		}
		for j := range got.Blocks {
			if got.Blocks[j] != first.Blocks[j] {
				t.Fatalf("run %d Blocks[%d] = %+v, first = %+v", i, j, got.Blocks[j], first.Blocks[j])
			}
		}
	}
}
