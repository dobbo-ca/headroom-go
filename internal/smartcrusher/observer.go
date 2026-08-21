package smartcrusher

import (
	"context"
	"log/slog"
)

// TracingObserver is the OSS-default Observer: a stateless debug-log emitter
// [ref: observer.rs].
//
// DEFERRED (Plan 4+): observability only, MVP-irrelevant to compression. The seam
// never plumbs observers, so OnEvent is not called on the compression path today;
// it is defined so WithDefaultOSSSetup can register the one default observer.
type TracingObserver struct{}

// Name reports the stable observer identifier.
func (TracingObserver) Name() string { return "tracing" }

// OnEvent debug-logs the crush event. It is zero-cost when debug logging is
// filtered (the level gate runs before any field evaluation) and MUST NOT panic —
// a nil event is tolerated.
func (TracingObserver) OnEvent(event *CrushEvent) {
	if event == nil {
		return
	}
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	slog.Debug("smart_crusher.crush emitted",
		"strategy", event.Strategy,
		"input_bytes", event.InputBytes,
		"output_bytes", event.OutputBytes,
		"elapsed_ns", event.ElapsedNs,
		"was_modified", event.WasModified,
	)
}
