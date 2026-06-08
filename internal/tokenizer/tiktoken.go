package tokenizer

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"
)

var offlineOnce sync.Once

// useOfflineVocab makes tiktoken load BPE ranks from the embedded offline loader
// instead of fetching them over the network — deterministic, no I/O at runtime.
func useOfflineVocab() {
	offlineOnce.Do(func() { tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader()) })
}

type tiktokenCounter struct {
	enc *tiktoken.Tiktoken
}

// newTiktoken returns a tiktoken-backed counter for the given encoding name
// (e.g. "cl100k_base"), or an error if the encoding can't be loaded.
func newTiktoken(encoding string) (*tiktokenCounter, error) {
	useOfflineVocab()
	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, err
	}
	return &tiktokenCounter{enc: enc}, nil
}

func (t *tiktokenCounter) CountText(text string) int {
	return len(t.enc.Encode(text, nil, nil))
}

func (t *tiktokenCounter) Backend() Backend { return BackendTiktoken }
