package signals

import "testing"

type fixed struct{ s ImportanceSignal }

func (f fixed) Score(string, ImportanceContext) ImportanceSignal { return f.s }

func TestTieredShortCircuitsOnConfidence(t *testing.T) {
	hi := fixed{Matched(Error, 0.95, 0.7)}
	lo := fixed{ImportanceSignal{Confidence: 0.4}}
	got := NewTiered().With(lo).With(hi).Score("x", Text)
	if !got.IsMatch() || got.Confidence != 0.7 {
		t.Fatalf("expected hi tier (conf 0.7) to win, got %+v", got)
	}
}

func TestTieredFallsToBest(t *testing.T) {
	a := fixed{ImportanceSignal{Confidence: 0.3}}
	b := fixed{ImportanceSignal{Confidence: 0.5}}
	got := NewTiered().With(a).With(b).Score("x", Text)
	if got.Confidence != 0.5 {
		t.Fatalf("expected best-confidence 0.5, got %v", got.Confidence)
	}
}

func TestTieredAllMissReturnsNeutral(t *testing.T) {
	got := NewTiered().Score("x", Text)
	if got.IsMatch() || got.Confidence != 0 || got.Priority != 0 {
		t.Fatalf("empty tiered must return neutral, got %+v", got)
	}
}

var _ LineImportanceDetector = NewTiered()
