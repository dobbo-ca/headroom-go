package policy

import "testing"

// The per-mode table is the only consumer these five fields have in v0.2, so
// this test asserts every field of every row by value. A row swap or a single
// flipped bool must fail here.
func TestForModeTable(t *testing.T) {
	tests := []struct {
		mode AuthMode
		want CompressionPolicy
	}{
		{PAYG, CompressionPolicy{
			LiveZoneOnly:           false,
			CacheAlignerEnabled:    true,
			VolatileTokenThreshold: 128,
			MaxLossyRatio:          0.45,
			ToinReadOnly:           false,
		}},
		// Identical to PAYG upstream in F2.1/F2.2. This row is the canary
		// that forces a deliberate update if the two ever diverge.
		{OAuth, CompressionPolicy{
			LiveZoneOnly:           false,
			CacheAlignerEnabled:    true,
			VolatileTokenThreshold: 128,
			MaxLossyRatio:          0.45,
			ToinReadOnly:           false,
		}},
		// Subscription is the conservative row: cache stability over savings.
		{Subscription, CompressionPolicy{
			LiveZoneOnly:           true,
			CacheAlignerEnabled:    false,
			VolatileTokenThreshold: 32,
			MaxLossyRatio:          0.25,
			ToinReadOnly:           true,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			if got := ForMode(tt.mode); got != tt.want {
				t.Errorf("ForMode(%v) = %+v, want %+v", tt.mode, got, tt.want)
			}
		})
	}
}

// Subscription must differ from PAYG on every field. If a future edit
// collapses them, the user-visible conservative mode has silently vanished.
func TestSubscriptionDiffersFromPAYGOnEveryField(t *testing.T) {
	p, s := ForMode(PAYG), ForMode(Subscription)
	if p.LiveZoneOnly == s.LiveZoneOnly {
		t.Error("LiveZoneOnly is identical across PAYG and Subscription")
	}
	if p.CacheAlignerEnabled == s.CacheAlignerEnabled {
		t.Error("CacheAlignerEnabled is identical across PAYG and Subscription")
	}
	if p.VolatileTokenThreshold == s.VolatileTokenThreshold {
		t.Error("VolatileTokenThreshold is identical across PAYG and Subscription")
	}
	if p.MaxLossyRatio == s.MaxLossyRatio {
		t.Error("MaxLossyRatio is identical across PAYG and Subscription")
	}
	if p.ToinReadOnly == s.ToinReadOnly {
		t.Error("ToinReadOnly is identical across PAYG and Subscription")
	}
}

// Subscription must be strictly more conservative on both numeric knobs.
func TestSubscriptionIsMoreConservative(t *testing.T) {
	p, s := ForMode(PAYG), ForMode(Subscription)
	if s.VolatileTokenThreshold >= p.VolatileTokenThreshold {
		t.Errorf("Subscription VolatileTokenThreshold %d must be below PAYG %d",
			s.VolatileTokenThreshold, p.VolatileTokenThreshold)
	}
	if s.MaxLossyRatio >= p.MaxLossyRatio {
		t.Errorf("Subscription MaxLossyRatio %v must be below PAYG %v",
			s.MaxLossyRatio, p.MaxLossyRatio)
	}
}

// An out-of-range AuthMode must not panic and must not silently return the
// zero CompressionPolicy, which would disable every knob at once.
func TestForModeUnknownFallsBackToPAYG(t *testing.T) {
	if got := ForMode(AuthMode(99)); got != ForMode(PAYG) {
		t.Errorf("ForMode(99) = %+v, want the PAYG row", got)
	}
}

func TestDefaultPAYG(t *testing.T) {
	if DefaultPAYG() != ForMode(PAYG) {
		t.Error("DefaultPAYG must equal ForMode(PAYG)")
	}
}
