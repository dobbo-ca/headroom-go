package offloads

import (
	"strings"
	"testing"
)

// Upstream json_offload.estimate_bloat uses Rust content.trim_start() (full
// Unicode whitespace) before the '[' prefix check. A 4-byte ASCII cutset would
// miss a vertical-tab/form-feed prefix and wrongly score 0.
func TestJsonOffloadBloatUnicodeTrim(t *testing.T) {
	body := "\v[" + strings.Repeat(`{"a":1},`, 9) + `{"a":1}]`
	if got := (&JsonOffload{minArrayRows: 5, saturationRows: 50}).EstimateBloat(body); got <= 0 {
		t.Fatalf("vertical-tab-prefixed JSON array must score > 0, got %v", got)
	}
}
