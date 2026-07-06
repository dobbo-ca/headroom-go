package smartcrusher

import "github.com/iancoleman/orderedmap"

// The OSS-default must-keep constraint stack [ref: constraints.rs]. These are NOT
// depth/size guards (MAX_DEPTH lives in the crusher, not here) — they are the
// error/outlier preservation seam from spec 8.5. Both are stateless empty structs
// whose MustKeep bodies are pure delegations to the outliers.go detectors.

// KeepErrorsConstraint preserves items whose JSON contains an error keyword. It
// USES the itemStrings cache when provided (perf only; the result is identical to
// the nil path) [ref: constraints.rs keep_errors].
type KeepErrorsConstraint struct{}

// Name reports the stable constraint identifier.
func (KeepErrorsConstraint) Name() string { return "keep_errors" }

// MustKeep delegates to DetectErrorItemsForPreservation, forwarding the optional
// itemStrings cache.
func (KeepErrorsConstraint) MustKeep(items []*orderedmap.OrderedMap, itemStrings []string) []int {
	return DetectErrorItemsForPreservation(items, itemStrings)
}

// KeepStructuralOutliersConstraint preserves items that look structurally
// anomalous (rare field or rare status value). It IGNORES itemStrings (upstream
// param is _item_strings) [ref: constraints.rs keep_structural_outliers].
type KeepStructuralOutliersConstraint struct{}

// Name reports the stable constraint identifier.
func (KeepStructuralOutliersConstraint) Name() string { return "keep_structural_outliers" }

// MustKeep delegates to DetectStructuralOutliers; the itemStrings argument is
// deliberately unused.
func (KeepStructuralOutliersConstraint) MustKeep(items []*orderedmap.OrderedMap, _ []string) []int {
	return DetectStructuralOutliers(items)
}

// defaultOSSConstraints returns the OSS-default constraint stack in fixed order:
// KeepErrors then KeepStructuralOutliers [ref: constraints.rs default_oss_
// constraints]. Order is preserved for observer/union determinism.
func defaultOSSConstraints() []Constraint {
	return []Constraint{
		KeepErrorsConstraint{},
		KeepStructuralOutliersConstraint{},
	}
}
