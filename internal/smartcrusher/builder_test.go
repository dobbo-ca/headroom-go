package smartcrusher

import "testing"

func TestBuilder_EmptyBuildsWithDefaultScorer(t *testing.T) {
	b := NewSmartCrusherBuilder(DefaultConfig())
	if len(b.constraints) != 0 {
		t.Fatalf("empty builder constraints = %d, want 0", len(b.constraints))
	}
	if len(b.observers) != 0 {
		t.Fatalf("empty builder observers = %d, want 0", len(b.observers))
	}
	if b.scorer != nil {
		t.Fatalf("empty builder scorer should be unset before Build()")
	}
	sc := b.Build()
	if sc.scorer == nil {
		t.Fatalf("Build() left scorer nil; expected HybridScorer default")
	}
	if len(sc.constraints) != 0 {
		t.Fatalf("built crusher constraints = %d, want 0", len(sc.constraints))
	}
	if len(sc.observers) != 0 {
		t.Fatalf("built crusher observers = %d, want 0", len(sc.observers))
	}
}

func TestBuilder_AddDefaultOSSConstraintsAppendsTwo(t *testing.T) {
	b := NewSmartCrusherBuilder(DefaultConfig()).AddDefaultOSSConstraints()
	if len(b.constraints) != 2 {
		t.Fatalf("AddDefaultOSSConstraints -> %d constraints, want 2", len(b.constraints))
	}
	if b.constraints[0].Name() != "keep_errors" {
		t.Fatalf("constraint[0] = %q, want keep_errors", b.constraints[0].Name())
	}
	if b.constraints[1].Name() != "keep_structural_outliers" {
		t.Fatalf("constraint[1] = %q, want keep_structural_outliers", b.constraints[1].Name())
	}
}

func TestBuilder_AddConstraintPreservesOrder(t *testing.T) {
	b := NewSmartCrusherBuilder(DefaultConfig()).
		AddConstraint(KeepStructuralOutliersConstraint{}).
		AddConstraint(KeepErrorsConstraint{})
	if len(b.constraints) != 2 {
		t.Fatalf("constraints = %d, want 2", len(b.constraints))
	}
	if b.constraints[0].Name() != "keep_structural_outliers" || b.constraints[1].Name() != "keep_errors" {
		t.Fatalf("order not preserved: %q, %q", b.constraints[0].Name(), b.constraints[1].Name())
	}
}

func TestBuilder_WithDefaultOSSSetupYieldsTwoConstraintsOneObserver(t *testing.T) {
	sc := NewSmartCrusherBuilder(DefaultConfig()).WithDefaultOSSSetup().Build()
	if len(sc.constraints) != 2 {
		t.Fatalf("constraints = %d, want 2", len(sc.constraints))
	}
	if len(sc.observers) != 1 {
		t.Fatalf("observers = %d, want 1", len(sc.observers))
	}
	if sc.observers[0].Name() != "tracing" {
		t.Fatalf("observer = %q, want tracing", sc.observers[0].Name())
	}
	if sc.scorer == nil {
		t.Fatalf("scorer nil after WithDefaultOSSSetup")
	}
}

func TestBuilder_WithDefaultCCRStoreYieldsWorkingStore(t *testing.T) {
	sc := NewSmartCrusherBuilder(DefaultConfig()).WithDefaultCCRStore().Build()
	if sc.ccrStore == nil {
		t.Fatalf("ccrStore nil after WithDefaultCCRStore (blank import registration failed?)")
	}
	if sc.ccrStore.Len() != 0 {
		t.Fatalf("fresh store Len = %d, want 0", sc.ccrStore.Len())
	}
	sc.ccrStore.Put("h", "v")
	got, ok := sc.ccrStore.Get("h")
	if !ok || got != "v" {
		t.Fatalf("store round-trip failed: got %q, ok=%v", got, ok)
	}
	if sc.ccrStore.Len() != 1 {
		t.Fatalf("store Len after Put = %d, want 1", sc.ccrStore.Len())
	}
}

func TestBuilder_AnchorConfigAndCompactionSetters(t *testing.T) {
	b := NewSmartCrusherBuilder(DefaultConfig()).
		AnchorConfig(AnchorConfig{FirstAnchors: 5, LastAnchors: 3}).
		WithDefaultCompaction()
	if b.anchorConfig == nil || b.anchorConfig.FirstAnchors != 5 {
		t.Fatalf("AnchorConfig setter did not store config")
	}
	if b.compaction == nil {
		t.Fatalf("WithDefaultCompaction did not set compaction stage")
	}
	sc := b.Build()
	if sc.compaction == nil {
		t.Fatalf("built crusher missing compaction stage")
	}
}
