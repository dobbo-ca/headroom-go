package tokenizer

import "testing"

func TestEstimatorRoundHalfUpRunes(t *testing.T) {
	// cpt = 4.0. "abcd" = 4 runes -> 1 token. "abcde" = 5 runes -> round(1.25)=1.
	// "abcdef" = 6 runes -> round(1.5) = 2 (round half up).
	c := EstimatingCounter{CharsPerToken: 4.0}
	cases := map[string]int{"": 1, "a": 1, "abcd": 1, "abcde": 1, "abcdef": 2, "abcdefgh": 2}
	for in, want := range cases {
		if got := c.CountText(in); got != want {
			t.Errorf("CountText(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestEstimatorCountsRunesNotBytes(t *testing.T) {
	c := EstimatingCounter{CharsPerToken: 4.0}
	// "é" is 2 bytes but 1 rune; 4 of them = 4 runes -> 1 token, not 2.
	if got := c.CountText("éééé"); got != 1 {
		t.Errorf("CountText(4 runes) = %d, want 1 (rune-based)", got)
	}
}

func TestEstimatorBackend(t *testing.T) {
	if (EstimatingCounter{CharsPerToken: 4}).Backend() != BackendEstimation {
		t.Fatal("estimator must report BackendEstimation")
	}
}
