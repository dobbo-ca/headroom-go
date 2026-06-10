package signals

// Tiered is a combinator over ordered LineImportanceDetector tiers. It scores by
// short-circuit escalation: the first tier whose signal confidence is at least
// the escalate threshold (0.7) wins immediately; otherwise the highest-confidence
// signal seen wins; if all tiers miss, Neutral(). Escalation compares confidence,
// not priority.
type Tiered struct {
	tiers []LineImportanceDetector
}

// NewTiered returns an empty Tiered combinator (no default tiers).
func NewTiered() *Tiered { return &Tiered{} }

// With appends a detector tier and returns the combinator for chaining.
func (t *Tiered) With(d LineImportanceDetector) *Tiered {
	t.tiers = append(t.tiers, d)
	return t
}

// Score evaluates each tier in insertion order. It returns the first tier whose
// confidence >= 0.7, otherwise the highest-confidence signal seen, otherwise
// Neutral().
func (t *Tiered) Score(line string, ctx ImportanceContext) ImportanceSignal {
	best := Neutral()
	for _, tier := range t.tiers {
		sig := tier.Score(line, ctx)
		if sig.Confidence >= escalateThreshold {
			return sig
		}
		if sig.Confidence > best.Confidence {
			best = sig
		}
	}
	return best
}
