package smartcrusher

// Task 15 — Decision-B acceptance suite (NOT byte-parity). Each testdata/fixtures/
// *.json fixture records an input `content`, the crush path it exercises, any config
// override, and behavioral expectations. The harness replays the fixture and asserts
// the Decision-B invariants only [ref: PARITY_FIXTURES_AND_TESTS goPortNotes]:
//
//	(1) WasModified exact;
//	(2) empty + passthrough byte-exact;
//	(3) ratio bound len(Compressed) <= len(Original), within ~10% of the recorded
//	    compressed length;
//	(4) round-trip losslessness — reconstruct-from-CcrGet == fixture input parsed —
//	    ONLY for fixtures whose marker is store-written (lossy space-form drops and
//	    walker comma-form blobs);
//	(5) kept inline rows are a verbatim subset of the original.
//
// Marker hashes are NOT byte-compared (Decision B picks its own stable hash fn); they
// are matched STRUCTURALLY (prefix "<<ccr:", suffix "N_rows_offloaded" for lossy
// drops, ",base64," for blobs, N == original-len minus kept-non-sentinel).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// fixtureConfig carries the config overrides a fixture needs (nil pointer == use the
// default). Only the knobs the acceptance suite exercises are represented.
type fixtureConfig struct {
	LosslessMinSavingsRatio *float64 `json:"losslessMinSavingsRatio,omitempty"`
	MaxItemsAfterCrush      *int     `json:"maxItemsAfterCrush,omitempty"`
	EnableCCRMarker         *bool    `json:"enableCCRMarker,omitempty"`
}

// fixtureFile is the on-disk fixture schema (testdata/fixtures/*.json). The recorded
// fields describe the CORRECT (Decision-B) behavior of the current crusher, not
// upstream byte output.
type fixtureFile struct {
	Name    string         `json:"name"`
	Path    string         `json:"path"` // "lossy" | "default" | "document"
	Content string         `json:"content"`
	Config  *fixtureConfig `json:"config,omitempty"`

	WasModified           bool   `json:"wasModified"`
	RecordedCompressedLen int    `json:"recordedCompressedLen"`
	RecordedCompressed    string `json:"recordedCompressed,omitempty"` // set when ByteExact expects a specific string

	// Assertion switches:
	ByteExact        bool   `json:"byteExact,omitempty"`        // empty/passthrough: compare exact
	AssertRoundTrip  bool   `json:"assertRoundTrip,omitempty"`  // store-written marker: CcrGet round-trips
	KeptSubsetInline bool   `json:"keptSubsetInline,omitempty"` // kept inline rows are a verbatim subset
	HasDropSentinel  bool   `json:"hasDropSentinel,omitempty"`  // last element is {"_ccr_dropped": space-form}
	MarkerVisible    bool   `json:"markerVisible,omitempty"`    // Compressed contains a lossy space-form marker
	BlobMarker       bool   `json:"blobMarker,omitempty"`       // a field holds a ",base64," comma-form blob marker
	SubArrayToString bool   `json:"subArrayToString,omitempty"` // a sub-array field became a rendered string
	RatioBoundOnly   bool   `json:"ratioBoundOnly,omitempty"`   // DEFERRED per-type crusher: assert bound only
	AllNumeric       bool   `json:"allNumeric,omitempty"`       // number path: output stays all-numeric
	NeverPassthrough bool   `json:"neverPassthrough,omitempty"` // below-threshold: strategy != passthrough
	StrategyPrefix   string `json:"strategyPrefix,omitempty"`   // strategy info startsWith
	SkipReason       string `json:"skipReason,omitempty"`       // strategy info startsWith (skip:*)
}

// configFor materializes the fixture's Config from DefaultConfig + overrides.
func (f fixtureFile) configFor() Config {
	cfg := DefaultConfig()
	if f.Config != nil {
		if f.Config.LosslessMinSavingsRatio != nil {
			cfg.LosslessMinSavingsRatio = *f.Config.LosslessMinSavingsRatio
		}
		if f.Config.MaxItemsAfterCrush != nil {
			cfg.MaxItemsAfterCrush = *f.Config.MaxItemsAfterCrush
		}
		if f.Config.EnableCCRMarker != nil {
			cfg.EnableCCRMarker = *f.Config.EnableCCRMarker
		}
	}
	return cfg
}

// crush runs the fixture through the appropriate crusher and returns the crushed
// output, WasModified, the strategy info, and the crusher (to read its CCR store).
func (f fixtureFile) crush(t *testing.T) (crushed string, wasModified bool, info string, sc *SmartCrusher) {
	t.Helper()
	cfg := f.configFor()
	switch f.Path {
	case "lossy":
		sc = NewSmartCrusherWithoutCompaction(cfg)
		crushed, wasModified, info = sc.smartCrushContent(f.Content, "", 0.0)
	case "default":
		sc = NewSmartCrusher(cfg)
		crushed, wasModified, info = sc.smartCrushContent(f.Content, "", 0.0)
	case "document":
		sc = NewSmartCrusher(cfg)
		var err error
		crushed, err = sc.CompactDocumentJSON(f.Content)
		if err != nil {
			t.Fatalf("%s: CompactDocumentJSON: %v", f.Name, err)
		}
		wasModified = crushed != strings.TrimSpace(f.Content)
	default:
		t.Fatalf("%s: unknown path %q", f.Name, f.Path)
	}
	return crushed, wasModified, info, sc
}

// loadFixtures reads every testdata/fixtures/*.json file into a fixtureFile.
func loadFixtures(t *testing.T) []fixtureFile {
	t.Helper()
	dir := filepath.Join("testdata", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	var out []fixtureFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", e.Name(), err)
		}
		f, err := unmarshalFixture(b)
		if err != nil {
			t.Fatalf("parse fixture %s: %v", e.Name(), err)
		}
		out = append(out, f)
	}
	return out
}

func TestParityFixtures_Count(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures) < 17 {
		t.Fatalf("fixture count = %d, want >= 17 (regenerate with -tags genfixtures)", len(fixtures))
	}
}

func TestParityFixtures(t *testing.T) {
	fixtures := loadFixtures(t)
	for _, f := range fixtures {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			crushed, mod, info, sc := f.crush(t)

			// (1) WasModified exact.
			if mod != f.WasModified {
				t.Fatalf("WasModified = %v, want %v (info=%q)", mod, f.WasModified, info)
			}

			// (2) empty + passthrough byte-exact.
			if f.ByteExact {
				want := f.RecordedCompressed
				if want == "" {
					want = f.Content
				}
				if crushed != want {
					t.Fatalf("byte-exact mismatch: got %q, want %q", crushed, want)
				}
			}
			if f.RecordedCompressed != "" && !f.ByteExact {
				// short_array_passthrough records the exact normalized form too.
				if crushed != f.RecordedCompressed {
					t.Fatalf("recorded-compressed mismatch: got %q, want %q", crushed, f.RecordedCompressed)
				}
			}

			// (3) ratio bound: never larger than the original, within ~10% of recorded.
			if len(crushed) > len(f.Content) {
				t.Fatalf("compressed longer than original: %d > %d", len(crushed), len(f.Content))
			}
			assertWithinTolerance(t, len(crushed), f.RecordedCompressedLen)

			// Strategy prefix / skip-reason assertions (behavior, not bytes).
			if f.StrategyPrefix != "" && !strings.HasPrefix(info, f.StrategyPrefix) {
				t.Fatalf("strategy info = %q, want prefix %q", info, f.StrategyPrefix)
			}
			if f.SkipReason != "" && !strings.HasPrefix(info, f.SkipReason) {
				t.Fatalf("strategy info = %q, want skip-reason prefix %q", info, f.SkipReason)
			}
			if f.NeverPassthrough {
				if info == "" || strings.HasPrefix(info, "none:") || strings.HasPrefix(info, "passthrough") {
					t.Fatalf("below-threshold fixture must not passthrough, info=%q", info)
				}
			}

			switch f.Path {
			case "lossy", "default":
				assertArrayFixture(t, f, crushed, sc)
			case "document":
				assertDocumentFixture(t, f, crushed, sc)
			}
		})
	}
}

// assertArrayFixture checks the array-crush invariants: drop sentinel shape, kept
// inline verbatim subset, all-numeric preservation, marker visibility, and the
// store-written round-trip.
func assertArrayFixture(t *testing.T, f fixtureFile, crushed string, sc *SmartCrusher) {
	t.Helper()
	origItems, origErr := decodeArrayTest(f.Content)

	decoded, err := decodeJSON(crushed)
	if err != nil {
		t.Fatalf("%s: crushed output not valid JSON: %v", f.Name, err)
	}

	// all-numeric preservation (number path).
	if f.AllNumeric {
		arr, ok := decoded.([]any)
		if !ok {
			t.Fatalf("%s: all-numeric fixture did not decode to an array", f.Name)
		}
		for i, it := range arr {
			if _, isNum := it.(interface{ Int64() (int64, error) }); !isNum {
				if _, isNumber := numberOf(it); !isNumber {
					t.Fatalf("%s: element %d is not numeric: %T", f.Name, i, it)
				}
			}
		}
	}

	arr, isArr := decoded.([]any)

	// drop sentinel: last element {"_ccr_dropped": "<<ccr:HASH N_rows_offloaded>>"}.
	if f.HasDropSentinel {
		if !isArr || len(arr) == 0 {
			t.Fatalf("%s: expected an array with a drop sentinel", f.Name)
		}
		hash, dropped := assertDropSentinel(t, f.Name, arr)
		// N == len(original) - len(kept-non-sentinel).
		keptNonSentinel := len(arr) - 1
		if origErr == nil {
			if dropped != len(origItems)-keptNonSentinel {
				t.Fatalf("%s: sentinel N=%d, want %d (orig=%d kept=%d)",
					f.Name, dropped, len(origItems)-keptNonSentinel, len(origItems), keptNonSentinel)
			}
		}
		// (4) round-trip: the store holds the byte-exact canonical original.
		if f.AssertRoundTrip {
			assertLossyRoundTrip(t, f, sc, hash, origItems)
		}
		// (5) kept inline rows are a verbatim subset of the original.
		if f.KeptSubsetInline && origErr == nil {
			assertKeptSubset(t, f.Name, arr[:len(arr)-1], origItems)
		}
	}

	// marker visibility (ccr_marker_visible fixture): the DEFAULT crusher at the
	// 0.99 gate must emit a space-form lossy marker in the output. It surfaces as
	// the last-element _ccr_dropped sentinel. We assert on the DECODED array so the
	// structural check is independent of string quoting; the serializer now emits the
	// literal "<<ccr:… rows_offloaded>>" marker verbatim (no HTML escaping of the
	// angle brackets) — see TestCompactWriteNoHTMLEscape and the crush-path marker
	// regression tests in jsonutil_test.go.
	if f.MarkerVisible {
		if !isArr || len(arr) == 0 {
			t.Fatalf("%s: marker-visible fixture did not decode to a non-empty array", f.Name)
		}
		assertDropSentinel(t, f.Name, arr) // validates the "<<ccr:HASH N_rows_offloaded>>" shape.
	}
}

// assertDocumentFixture checks the walker-path invariants: a ",base64," blob marker
// in some field (store-written, round-trips) and a sub-array collapsed to a string.
func assertDocumentFixture(t *testing.T, f fixtureFile, crushed string, sc *SmartCrusher) {
	t.Helper()
	decoded, err := decodeJSON(crushed)
	if err != nil {
		t.Fatalf("%s: document output not valid JSON: %v", f.Name, err)
	}
	om, ok := decoded.(*orderedmap.OrderedMap)
	if !ok {
		t.Fatalf("%s: document output is not an object: %T", f.Name, decoded)
	}

	if f.BlobMarker {
		marker, field := findBlobMarker(om)
		if marker == "" {
			t.Fatalf("%s: no ',base64,' blob marker field found", f.Name)
		}
		if !strings.HasPrefix(marker, "<<ccr:") || !strings.Contains(marker, ",base64,") || !strings.HasSuffix(marker, ">>") {
			t.Fatalf("%s: blob marker malformed in field %q: %q", f.Name, field, marker)
		}
		if f.AssertRoundTrip {
			hash := blobHash(marker)
			payload, ok := sc.CcrGet(hash)
			if !ok {
				t.Fatalf("%s: CcrGet(%q) miss for blob marker", f.Name, hash)
			}
			// The blob field's original value is the payload byte-exact.
			origObj, err := decodeJSON(f.Content)
			if err != nil {
				t.Fatalf("%s: decode fixture content: %v", f.Name, err)
			}
			origBlob, _ := origObj.(*orderedmap.OrderedMap).Get(field)
			if s, _ := origBlob.(string); s != payload {
				t.Fatalf("%s: round-trip blob mismatch for field %q", f.Name, field)
			}
			if sc.CcrLen() < 1 {
				t.Fatalf("%s: store did not grow for blob offload", f.Name)
			}
		}
	}

	if f.SubArrayToString {
		foundString := false
		for _, k := range om.Keys() {
			v, _ := om.Get(k)
			if s, ok := v.(string); ok && !strings.HasPrefix(s, "<<ccr:") {
				// A rendered CSV+schema sub-table string starts with "[N]{".
				if strings.HasPrefix(s, "[") && strings.Contains(s, "]{") {
					foundString = true
					break
				}
			}
		}
		if !foundString {
			t.Fatalf("%s: no sub-array collapsed to a rendered string", f.Name)
		}
	}
}

// TestParityCcrRoundtrip mirrors the upstream ccr_roundtrip focused test: a fresh
// store is empty and misses; passthrough/empty do not grow it; a lossy drop grows it
// by one; re-crushing the same payload is idempotent (same hash, no growth); distinct
// payloads produce distinct isolated entries; and CcrGet reconstructs the original.
func TestParityCcrRoundtrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxItemsAfterCrush = 15
	cfg.LosslessMinSavingsRatio = 0.99
	sc := NewSmartCrusher(cfg)

	// fresh: len 0, get miss.
	if sc.CcrLen() != 0 {
		t.Fatalf("fresh CcrLen = %d, want 0", sc.CcrLen())
	}
	if _, ok := sc.CcrGet("deadbeef0000"); ok {
		t.Fatalf("fresh CcrGet should miss")
	}

	// passthrough/empty: no growth.
	sc.Crush("[]", "", 0.0)
	sc.Crush("[1,2,3]", "", 0.0)
	if sc.CcrLen() != 0 {
		t.Fatalf("passthrough/empty grew store to %d, want 0", sc.CcrLen())
	}

	// lossy drop: grows by 1.
	itemsA, _ := decodeArrayTest(buildErrorDicts(30))
	rA := sc.crushArray(itemsA, "", 0.0)
	if rA.CcrHash == nil {
		t.Fatalf("lossy drop produced no hash")
	}
	if sc.CcrLen() != 1 {
		t.Fatalf("after lossy CcrLen = %d, want 1", sc.CcrLen())
	}

	// round-trip: stored payload reconstructs the original 30 items.
	payload, ok := sc.CcrGet(*rA.CcrHash)
	if !ok {
		t.Fatalf("CcrGet(%q) miss", *rA.CcrHash)
	}
	reparsed, err := decodeArrayTest(payload)
	if err != nil || len(reparsed) != 30 {
		t.Fatalf("stored payload reconstruct: len=%d err=%v", len(reparsed), err)
	}

	// idempotent: re-crush same payload -> same hash, no growth.
	itemsA2, _ := decodeArrayTest(buildErrorDicts(30))
	rA2 := sc.crushArray(itemsA2, "", 0.0)
	if rA2.CcrHash == nil || *rA2.CcrHash != *rA.CcrHash {
		t.Fatalf("re-crush hash changed: %v vs %v", rA2.CcrHash, rA.CcrHash)
	}
	if sc.CcrLen() != 1 {
		t.Fatalf("idempotent re-crush grew store to %d, want 1", sc.CcrLen())
	}

	// distinct payloads: isolated distinct hashes.
	itemsB, _ := decodeArrayTest(buildUniformDicts(30))
	rB := sc.crushArray(itemsB, "", 0.0)
	if rB.CcrHash == nil {
		t.Fatalf("second distinct lossy drop produced no hash")
	}
	if *rB.CcrHash == *rA.CcrHash {
		t.Fatalf("distinct payloads collided on hash %q", *rA.CcrHash)
	}
	if sc.CcrLen() != 2 {
		t.Fatalf("distinct payload store len = %d, want 2", sc.CcrLen())
	}
	// Isolation: each hash still returns its OWN original.
	pA, _ := sc.CcrGet(*rA.CcrHash)
	pB, _ := sc.CcrGet(*rB.CcrHash)
	if pA == pB {
		t.Fatalf("distinct entries returned equal payloads")
	}
}

// TestParityLosslessDefault mirrors the upstream lossless_default focused test: a
// uniform tabular array compacts to a lossless:table string under the DEFAULT
// crusher, and a below-threshold-unique array never passes through (lossy runs when
// lossless declines).
func TestParityLosslessDefault(t *testing.T) {
	sc := NewSmartCrusher(DefaultConfig())
	content := buildUniformDicts(50)
	crushed, mod, info := sc.smartCrushContent(content, "", 0.0)
	if !mod {
		t.Fatalf("uniform tabular default should be modified")
	}
	if len(crushed) >= len(content) {
		t.Fatalf("lossless table did not shrink: %d >= %d", len(crushed), len(content))
	}
	if !strings.HasPrefix(info, "lossless:table") {
		t.Fatalf("info = %q, want prefix lossless:table", info)
	}
	if sc.CcrLen() != 0 {
		t.Fatalf("lossless win wrote to store (Len=%d)", sc.CcrLen())
	}

	// below-threshold unique: never passthrough (lossy runs when lossless declines).
	scB := NewSmartCrusherWithoutCompaction(DefaultConfig())
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		writeUniqueDict(&b, i)
	}
	b.WriteByte(']')
	_, _, infoB := scB.smartCrushContent(b.String(), "", 0.0)
	if infoB == "" || strings.HasPrefix(infoB, "none:") || strings.HasPrefix(infoB, "passthrough") {
		t.Fatalf("below-threshold unique must not passthrough, info=%q", infoB)
	}
}
