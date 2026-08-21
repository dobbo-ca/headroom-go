package semcache

import (
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	"github.com/dobbo-ca/headroom-go/internal/embed"
)

// Options is the flat, config-file-shaped view of a Cache. The v0.2 gateway
// builds one of these from TOML and calls FromOptions; nothing else in the
// tree needs to know that embed and semcache are separate packages.
type Options struct {
	Enabled    bool
	Endpoint   string
	Model      string
	Threshold  float32
	MaxEntries int
	Timeout    time.Duration
}

// FromOptions builds a Cache backed by a live embedding client. Every unset
// field falls back to its documented default, and the zero Options yields a
// disabled cache.
func FromOptions(opts Options, store ccr.Store) *Cache {
	model := opts.Model
	if model == "" {
		model = embed.DefaultModel
	}
	client := embed.New(opts.Endpoint, model, opts.Timeout)
	return New(Config{
		Enabled:    opts.Enabled,
		Threshold:  opts.Threshold,
		MaxEntries: opts.MaxEntries,
		Model:      model,
	}, client, store)
}
