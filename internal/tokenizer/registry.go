package tokenizer

import "strings"

// GetTokenizer returns the best available tokenizer for a model. Resolution:
// OpenAI families -> tiktoken (cl100k_base/o200k_base), with estimator fallback
// if the offline vocab can't load; everything else -> the estimator (which
// covers Claude well). The HF backend is a follow-up.
func GetTokenizer(model string) Tokenizer {
	m := strings.ToLower(model)
	if enc := openAIEncoding(m); enc != "" {
		if t, err := newTiktoken(enc); err == nil {
			return t
		}
	}
	return EstimatingCounter{CharsPerToken: defaultCharsPerToken}
}

func openAIEncoding(model string) string {
	switch {
	case strings.HasPrefix(model, "gpt-4o"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"):
		return "o200k_base"
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3.5"):
		return "cl100k_base"
	default:
		return ""
	}
}
