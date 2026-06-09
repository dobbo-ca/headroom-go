// Package tokenizer counts tokens for compression ratio decisions. It ships a
// cheap rune-based estimator and a tiktoken backend; the HF backend is a
// follow-up. The estimator alone covers Claude (the primary target model).
package tokenizer

import "unicode/utf8"

// Backend identifies which counting strategy a Tokenizer uses.
type Backend int

const (
	BackendTiktoken Backend = iota
	BackendHuggingFace
	BackendEstimation
)

// Tokenizer counts tokens in text.
type Tokenizer interface {
	CountText(text string) int
	Backend() Backend
}

// EstimatingCounter approximates token count as runes / CharsPerToken, rounded
// half-up, with a floor of 1. Rune-based (not bytes) so multibyte text is not
// over-counted. Deterministic and dependency-free.
type EstimatingCounter struct {
	CharsPerToken float64
}

const defaultCharsPerToken = 4.0

func (e EstimatingCounter) CountText(text string) int {
	cpt := e.CharsPerToken
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}
	runes := utf8.RuneCountInString(text)
	n := int(float64(runes)/cpt + 0.5) // round half up
	if n < 1 {
		return 1
	}
	return n
}

func (e EstimatingCounter) Backend() Backend { return BackendEstimation }
