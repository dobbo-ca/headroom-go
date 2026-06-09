package pipeline

import (
	_ "embed"

	"github.com/BurntSushi/toml"
)

//go:embed pipeline.toml
var defaultConfigTOML string

// Config holds the orchestrator's gating thresholds.
type Config struct {
	ReformatTargetRatio  float64 `toml:"reformat_target_ratio"`
	BloatThreshold       float64 `toml:"bloat_threshold"`
	OffloadFallbackRatio float64 `toml:"offload_fallback_ratio"`
}

// DefaultConfig parses the embedded pipeline.toml.
func DefaultConfig() Config {
	var c Config
	if _, err := toml.Decode(defaultConfigTOML, &c); err != nil {
		// embedded constant is known-good; fall back to literals if it ever isn't.
		return Config{ReformatTargetRatio: 0.5, BloatThreshold: 0.5, OffloadFallbackRatio: 0.85}
	}
	return c
}
