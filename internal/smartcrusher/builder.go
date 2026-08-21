package smartcrusher

import (
	"github.com/dobbo-ca/headroom-go/internal/ccr"
	// Blank import registers the in-memory CCR backend's constructor via its
	// init() -> ccr.RegisterInMemory. Without it, ccr.FromConfig(Kind:InMemory)
	// returns "in-memory backend not registered" and the default crusher's store
	// construction fails at runtime. This is the ONE sanctioned widening of the
	// smartcrusher dependency surface beyond ccr/relevance (Shared Contract rule 5):
	// the package is meant to be linked with the in-memory backend registered, and
	// WithDefaultCCRStore relies on it so Build() can be error-free.
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/relevance"
	"github.com/dobbo-ca/headroom-go/internal/smartcrusher/compaction"
)

// SmartCrusherBuilder is the fluent composition point for a SmartCrusher [ref:
// builder.rs]. It carries zero compression logic — it only assembles the config,
// scorer, constraint/observer stacks, optional compaction stage, and optional CCR
// store, then wires them into a SmartCrusher via from_parts on Build(). A fresh
// builder is EMPTY (no scorer/constraints/observers/compaction/store); Build()
// defaults only the scorer (to HybridScorer). Setters return the builder for
// chaining and COMPOSE (append), never replace.
type SmartCrusherBuilder struct {
	config       Config
	anchorConfig *AnchorConfig
	scorer       Scorer
	constraints  []Constraint
	observers    []Observer
	compaction   *compaction.CompactionStage
	ccrStore     ccr.Store
}

// NewSmartCrusherBuilder returns an EMPTY builder holding config by value.
func NewSmartCrusherBuilder(config Config) *SmartCrusherBuilder {
	return &SmartCrusherBuilder{config: config}
}

// AnchorConfig sets the anchor head/tail counts (defaulted at Build() when unset).
func (b *SmartCrusherBuilder) AnchorConfig(cfg AnchorConfig) *SmartCrusherBuilder {
	b.anchorConfig = &cfg
	return b
}

// WithScorer plugs in a relevance scorer (enterprise seam). Build() falls back to
// HybridScorer when this is unset.
func (b *SmartCrusherBuilder) WithScorer(scorer Scorer) *SmartCrusherBuilder {
	b.scorer = scorer
	return b
}

// AddConstraint appends a must-keep constraint (order preserved for determinism).
func (b *SmartCrusherBuilder) AddConstraint(c Constraint) *SmartCrusherBuilder {
	b.constraints = append(b.constraints, c)
	return b
}

// AddDefaultOSSConstraints appends the OSS-default constraint stack
// [KeepErrors, KeepStructuralOutliers] in that order (composes, never replaces).
func (b *SmartCrusherBuilder) AddDefaultOSSConstraints() *SmartCrusherBuilder {
	b.constraints = append(b.constraints, defaultOSSConstraints()...)
	return b
}

// AddObserver appends an observer (fires in registration order).
func (b *SmartCrusherBuilder) AddObserver(o Observer) *SmartCrusherBuilder {
	b.observers = append(b.observers, o)
	return b
}

// WithDefaultOSSSetup wires the OSS defaults: HybridScorer, the two default
// constraints, and the TracingObserver. It equals SmartCrusher::new(config).
func (b *SmartCrusherBuilder) WithDefaultOSSSetup() *SmartCrusherBuilder {
	return b.
		WithScorer(relevance.NewHybridScorer()).
		AddDefaultOSSConstraints().
		AddObserver(TracingObserver{})
}

// WithCompaction sets the compaction stage (run before the lossy path).
func (b *SmartCrusherBuilder) WithCompaction(stage compaction.CompactionStage) *SmartCrusherBuilder {
	b.compaction = &stage
	return b
}

// WithDefaultCompaction sets the OSS-default CSV-schema compaction stage.
func (b *SmartCrusherBuilder) WithDefaultCompaction() *SmartCrusherBuilder {
	stage := compaction.DefaultCsvSchemaStage()
	b.compaction = &stage
	return b
}

// WithCCRStore sets an explicit CCR store.
func (b *SmartCrusherBuilder) WithCCRStore(store ccr.Store) *SmartCrusherBuilder {
	b.ccrStore = store
	return b
}

// WithDefaultCCRStore sets an in-memory CCR store (1000 entries / 5-min TTL). The
// blank import above guarantees the in-memory backend is registered, so store
// construction cannot fail; mustInMemoryStore panics only on the structurally
// impossible error.
func (b *SmartCrusherBuilder) WithDefaultCCRStore() *SmartCrusherBuilder {
	b.ccrStore = mustInMemoryStore()
	return b
}

// Build wires the assembled parts into a SmartCrusher [ref: builder.rs Build]. It
// returns NO error (builder.rs goPortNotes: defaults can't fail). It defaults the
// anchor config and scorer when unset, clones the config for the analyzer, and
// hands everything to from_parts.
func (b *SmartCrusherBuilder) Build() *SmartCrusher {
	anchorCfg := DefaultAnchorConfig()
	if b.anchorConfig != nil {
		anchorCfg = *b.anchorConfig
	}
	anchorSelector := NewAnchorSelector(anchorCfg)

	scorer := b.scorer
	if scorer == nil {
		scorer = relevance.NewHybridScorer()
	}

	analyzer := NewSmartAnalyzer(b.config)

	return fromParts(b.config, anchorSelector, scorer, analyzer, b.constraints, b.observers, b.compaction, b.ccrStore)
}

// mustInMemoryStore builds the default in-memory CCR store. The InMemory branch of
// ccr.FromConfig cannot fail once internal/ccr/backends is blank-imported (done at
// the top of this file), so an error here is a programmer error (broken linkage)
// and panics per the documented contract — keeping WithDefaultCCRStore/Build()
// error-free.
func mustInMemoryStore() ccr.Store {
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory})
	if err != nil {
		panic("smartcrusher: in-memory CCR backend not registered (blank import of internal/ccr/backends missing): " + err.Error())
	}
	return s
}

// SmartCrusher is the assembled crusher [ref: crusher.rs from_parts]. The struct
// and its fromParts constructor live here (in builder.go) because Build() needs to
// materialize it; the crush behavior (the offloads.Crusher seam impl, the 3-tier
// funnel, processValue, and the two public NewSmartCrusher* constructors) is added
// in crusher.go (Task 14). The planner field is derived inside fromParts from the
// other collaborators.
type SmartCrusher struct {
	config         Config
	anchorSelector *AnchorSelector
	scorer         Scorer
	analyzer       *SmartAnalyzer
	planner        *SmartCrusherPlanner
	constraints    []Constraint
	observers      []Observer
	compaction     *compaction.CompactionStage
	ccrStore       ccr.Store
}

// fromParts assembles a SmartCrusher from its collaborators, building the planner
// from the analyzer/scorer/anchorSelector/constraints [ref: crusher.rs from_parts].
func fromParts(config Config, anchorSelector *AnchorSelector, scorer Scorer, analyzer *SmartAnalyzer, constraints []Constraint, observers []Observer, compactionStage *compaction.CompactionStage, ccrStore ccr.Store) *SmartCrusher {
	planner := NewSmartCrusherPlanner(config, anchorSelector, scorer, analyzer, constraints)
	return &SmartCrusher{
		config:         config,
		anchorSelector: anchorSelector,
		scorer:         scorer,
		analyzer:       analyzer,
		planner:        planner,
		constraints:    constraints,
		observers:      observers,
		compaction:     compactionStage,
		ccrStore:       ccrStore,
	}
}
