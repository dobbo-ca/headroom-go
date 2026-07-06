package smartcrusher

// Structural helpers for the Decision-B parity harness: fixture unmarshalling, the
// ~10% ratio tolerance, the drop-sentinel / blob-marker structural matchers, and the
// verbatim-subset / round-trip asserters. None byte-compare a hash — markers are
// matched by SHAPE (prefix/suffix) and reconciled against the CCR store.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

// unmarshalFixture decodes a fixture file's JSON into a fixtureFile.
func unmarshalFixture(b []byte) (fixtureFile, error) {
	var f fixtureFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fixtureFile{}, err
	}
	if f.Name == "" {
		return fixtureFile{}, fmt.Errorf("fixture missing name")
	}
	return f, nil
}

// assertWithinTolerance asserts got is within ~10% of recorded (Decision-B ratio
// bound). A tolerance floor of 8 bytes covers tiny fixtures (e.g. "42", "[]") where a
// 10% window would round to zero.
func assertWithinTolerance(t *testing.T, got, recorded int) {
	t.Helper()
	tol := recorded / 10
	if tol < 8 {
		tol = 8
	}
	diff := got - recorded
	if diff < 0 {
		diff = -diff
	}
	if diff > tol {
		t.Fatalf("compressed length %d not within ~10%% of recorded %d (tol=%d)", got, recorded, tol)
	}
}

// assertDropSentinel verifies arr's LAST element is {"_ccr_dropped": marker} where
// marker is a space-form lossy marker "<<ccr:HASH N_rows_offloaded>>". It returns the
// extracted HASH and the parsed N.
func assertDropSentinel(t *testing.T, name string, arr []any) (hash string, dropped int) {
	t.Helper()
	last := arr[len(arr)-1]
	om, ok := last.(*orderedmap.OrderedMap)
	if !ok {
		t.Fatalf("%s: last element is not an object: %T", name, last)
	}
	v, present := om.Get("_ccr_dropped")
	if !present {
		t.Fatalf("%s: last element missing _ccr_dropped sentinel", name)
	}
	marker, _ := v.(string)
	if !strings.HasPrefix(marker, "<<ccr:") || !strings.HasSuffix(marker, "_rows_offloaded>>") {
		t.Fatalf("%s: drop marker malformed: %q", name, marker)
	}
	// Parse "<<ccr:HASH N_rows_offloaded>>".
	inner := strings.TrimPrefix(marker, "<<ccr:")
	inner = strings.TrimSuffix(inner, ">>")
	sp := strings.IndexByte(inner, ' ')
	if sp < 0 {
		t.Fatalf("%s: drop marker missing space separator: %q", name, marker)
	}
	hash = inner[:sp]
	if len(hash) != 12 {
		t.Fatalf("%s: space-form hash width = %d, want 12 (%q)", name, len(hash), hash)
	}
	nStr := strings.TrimSuffix(inner[sp+1:], "_rows_offloaded")
	n, err := strconv.Atoi(nStr)
	if err != nil {
		t.Fatalf("%s: drop marker N not an int: %q", name, marker)
	}
	return hash, n
}

// assertLossyRoundTrip verifies the CCR store holds the byte-exact canonical original
// under hash, and that the stored payload re-decodes to the same number of items as
// the fixture input.
func assertLossyRoundTrip(t *testing.T, f fixtureFile, sc *SmartCrusher, hash string, origItems []any) {
	t.Helper()
	payload, ok := sc.CcrGet(hash)
	if !ok {
		t.Fatalf("%s: CcrGet(%q) miss (lossy round-trip)", f.Name, hash)
	}
	reparsed, err := decodeArrayTest(payload)
	if err != nil {
		t.Fatalf("%s: stored lossy payload not a JSON array: %v", f.Name, err)
	}
	if len(reparsed) != len(origItems) {
		t.Fatalf("%s: stored payload has %d items, want %d", f.Name, len(reparsed), len(origItems))
	}
	// Element-wise structural equality against the original.
	for i := range reparsed {
		if compactSerialize(reparsed[i]) != compactSerialize(origItems[i]) {
			t.Fatalf("%s: stored payload element %d differs from original", f.Name, i)
		}
	}
}

// assertKeptSubset verifies every kept inline element (arr, excluding the sentinel)
// is a verbatim (structural) member of the original items.
func assertKeptSubset(t *testing.T, name string, kept, origItems []any) {
	t.Helper()
	origSet := make(map[string]struct{}, len(origItems))
	for _, it := range origItems {
		origSet[compactSerialize(it)] = struct{}{}
	}
	for i, it := range kept {
		if _, ok := origSet[compactSerialize(it)]; !ok {
			t.Fatalf("%s: kept element %d is not a verbatim subset of the original", name, i)
		}
	}
}

// findBlobMarker scans om's values (one level deep) for a ",base64," comma-form CCR
// blob marker, returning the marker string and its field key ("" if none).
func findBlobMarker(om *orderedmap.OrderedMap) (marker, field string) {
	for _, k := range om.Keys() {
		v, _ := om.Get(k)
		if s, ok := v.(string); ok && strings.HasPrefix(s, "<<ccr:") && strings.Contains(s, ",base64,") {
			return s, k
		}
	}
	return "", ""
}

// blobHash extracts the HASH from a comma-form blob marker "<<ccr:HASH,base64,SIZE>>".
func blobHash(marker string) string {
	inner := strings.TrimPrefix(marker, "<<ccr:")
	if c := strings.IndexByte(inner, ','); c >= 0 {
		return inner[:c]
	}
	return inner
}

// numberOf reports whether v is a JSON number (json.Number), returning its string
// form. Used by the all-numeric preservation check.
func numberOf(v any) (string, bool) {
	if n, ok := v.(json.Number); ok {
		return n.String(), true
	}
	return "", false
}

// writeUniqueDict appends a distinct-per-i dict so an array of them is high-uniqueness
// (used by the below-threshold "never passthrough" check).
func writeUniqueDict(b *strings.Builder, i int) {
	fmt.Fprintf(b, `{"uid":"x-%d-%d","blob":"unique-payload-%d-%d"}`, i, i*13, i, i*7)
}
