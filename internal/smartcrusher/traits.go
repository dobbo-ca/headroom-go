package smartcrusher

import (
	"github.com/dobbo-ca/headroom-go/internal/relevance"
	"github.com/iancoleman/orderedmap"
)

// Extension-surface traits [ref: traits.rs]. The heavy compression machinery does
// NOT live here — this file only carries the small interfaces the OSS-default
// stack composes over: Constraint (error/outlier/anchor preservation, spec 8.5),
// the DEFERRED Observer/CrushEvent telemetry seam, and the Scorer alias. All of
// these stay INTERNAL to smartcrusher; none is part of the offloads.Crusher seam
// (Shared Contract rule 5).

// Constraint yields the in-bounds item indices that MUST be kept. The allocator
// takes the union across the registered constraint stack. itemStrings is an
// optional pre-serialized JSON cache (nil when absent); it is a perf hint only and
// never changes the result. Implementations are pure and O(n) and must never
// return an out-of-bounds index (the allocator would silently drop it anyway).
type Constraint interface {
	Name() string
	MustKeep(items []*orderedmap.OrderedMap, itemStrings []string) []int
}

// CrushEvent is the per-Crush telemetry record consumed by Observers. All byte
// fields are BYTE lengths (Go len), NOT rune counts [ref: traits.rs].
//
// DEFERRED (Plan 4+): only TracingObserver consumes this today; the seam carries
// no observer wiring, so the compression path never populates or emits it.
type CrushEvent struct {
	Strategy    string
	InputBytes  int
	OutputBytes int
	ElapsedNs   uint64
	WasModified bool
}

// Observer receives one CrushEvent per top-level Crush, synchronously, after the
// result is computed and before it is returned. Observers fire in registration
// order and MUST NOT panic (catch any I/O error internally).
//
// DEFERRED (Plan 4+): telemetry only; the MVP seam does not plumb observers.
type Observer interface {
	Name() string
	OnEvent(event *CrushEvent)
}

// Scorer is the relevance scoring contract. It is an ALIAS of relevance.Scorer,
// not a redefinition — do NOT define a second Scorer interface [ref: traits.rs
// Scorer re-export].
type Scorer = relevance.Scorer
