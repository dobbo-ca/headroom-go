package policy

// Per-mode default values, ported from upstream. Operators tune by editing
// these constants and shipping a build; there is deliberately no env var per
// field.
const (
	// PAYG tolerates volatile content noise up to ~128 tokens before
	// flagging it; PAYG users opt in to aggressive compression.
	volatileTokenThresholdPAYG = 128
	// Subscription flags volatile content at 32 tokens so cache prefixes
	// stay stable.
	volatileTokenThresholdSubscription = 32
	// PAYG caps lossy compression at 45% of original tokens.
	maxLossyRatioPAYG = 0.45
	// Subscription caps at 25%: cache stability over savings.
	maxLossyRatioSubscription = 0.25
)

// CompressionPolicy is the per-auth-mode policy downstream compression
// stages consult.
//
// ponytail: all five fields are plumbed and none is enforced in v0.2 —
// matching upstream, where the same three are documented "plumbed but
// unconsumed". They are ported for struct parity so the cachestab and TOIN
// ports drop in without changing this type. ForMode's table is their only
// coverage; wire each field as its consumer lands.
type CompressionPolicy struct {
	// LiveZoneOnly forbids modifying bytes outside the post-cache-marker
	// live zone. The live-zone dispatcher is live-zone-only by
	// construction, so this is informational here; it exists for
	// transforms that inspect the cached prefix.
	LiveZoneOnly bool

	// CacheAlignerEnabled gates the CacheAligner transform. The core design
	// spec marks CacheAligner doc-only-skip, so nothing reads this yet.
	CacheAlignerEnabled bool

	// VolatileTokenThreshold is the token count below which content is
	// treated as cache-stable. Low means flag aggressively and keep prompts
	// stable; high means tolerate more volatile noise.
	VolatileTokenThreshold int

	// MaxLossyRatio bounds lossy compression as the fraction of original
	// tokens that may be dropped, in [0,1].
	MaxLossyRatio float64

	// ToinReadOnly serves cached patterns but writes no new observations
	// from this request.
	ToinReadOnly bool
}

// aggressive is the PAYG and OAuth row. Upstream treats the two as identical
// in F2.1/F2.2 and may diverge them once telemetry is collected; keeping one
// named value makes that divergence a deliberate edit.
var aggressive = CompressionPolicy{
	LiveZoneOnly:           false,
	CacheAlignerEnabled:    true,
	VolatileTokenThreshold: volatileTokenThresholdPAYG,
	MaxLossyRatio:          maxLossyRatioPAYG,
	ToinReadOnly:           false,
}

// conservative is the Subscription row: the user-visible win.
var conservative = CompressionPolicy{
	LiveZoneOnly:           true,
	CacheAlignerEnabled:    false,
	VolatileTokenThreshold: volatileTokenThresholdSubscription,
	MaxLossyRatio:          maxLossyRatioSubscription,
	ToinReadOnly:           true,
}

// ForMode resolves the policy for an auth mode. An unrecognised mode falls
// back to PAYG rather than the zero value, which would disable every knob.
func ForMode(m AuthMode) CompressionPolicy {
	switch m {
	case Subscription:
		return conservative
	case OAuth:
		return aggressive
	default:
		return aggressive
	}
}

// DefaultPAYG is the policy used on the unclassified path.
func DefaultPAYG() CompressionPolicy { return ForMode(PAYG) }
