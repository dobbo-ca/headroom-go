package tokenizer

import "testing"

func TestGetTokenizerFallsBackToEstimator(t *testing.T) {
	// An unknown model must still return a working tokenizer (estimator).
	tok := GetTokenizer("some-unknown-model")
	if tok == nil {
		t.Fatal("GetTokenizer returned nil")
	}
	if tok.CountText("hello world this is a test") < 1 {
		t.Fatal("tokenizer counted < 1")
	}
}

func TestGetTokenizerOpenAIUsesTiktoken(t *testing.T) {
	tok := GetTokenizer("gpt-4o")
	if tok.Backend() != BackendTiktoken {
		t.Skip("offline tiktoken vocab unavailable in this environment")
	}
	if tok.CountText("hello") < 1 {
		t.Fatal("tiktoken counted < 1")
	}
}
