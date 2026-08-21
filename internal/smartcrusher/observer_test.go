package smartcrusher

import (
	"github.com/dobbo-ca/headroom-go/internal/relevance"

	"testing"
)

// [ref: observer.rs edgeCases] TracingObserver is DEFERRED telemetry: it names
// itself "tracing" and its OnEvent must never panic (including on a nil event or
// zero-valued fields).

func TestTracingObserverName(t *testing.T) {
	if got := (TracingObserver{}).Name(); got != "tracing" {
		t.Fatalf("Name() = %q, want %q", got, "tracing")
	}
}

func TestTracingObserverOnEventDoesNotPanic(t *testing.T) {
	var o Observer = TracingObserver{}
	// Populated event.
	o.OnEvent(&CrushEvent{
		Strategy:    "smart_sample",
		InputBytes:  100,
		OutputBytes: 40,
		ElapsedNs:   1234,
		WasModified: true,
	})
	// Zero-valued event (empty strategy, zero bytes/elapsed).
	o.OnEvent(&CrushEvent{})
	// Nil event must be tolerated.
	o.OnEvent(nil)
}

// Compile-time guard: the package Scorer name is the relevance.Scorer alias, not
// a redefinition [ref: traits.rs Scorer re-export].
func TestScorerAliasIdentity(t *testing.T) {
	var s Scorer = relevance.NewHybridScorer()
	if !s.IsAvailable() {
		t.Fatalf("HybridScorer via Scorer alias reports unavailable")
	}
	var _ relevance.Scorer = s // assignable both directions => same interface.
}
