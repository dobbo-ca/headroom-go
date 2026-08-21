package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends" // register the in-memory store for round-trip tests
	"github.com/iancoleman/orderedmap"
)

// newTestStore builds a fresh in-memory CCR store for round-trip assertions.
func newTestStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory})
	if err != nil {
		t.Fatalf("ccr.FromConfig(InMemory) error: %v", err)
	}
	return s
}

func TestWalkerEmptyContainersUnchanged(t *testing.T) {
	// Empty object {} and empty array [] pass through unchanged (compact declines).
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}

	emptyObj := decode(t, `{}`)
	gotObj, ok := d.Compact(emptyObj).(*orderedmap.OrderedMap)
	if !ok || len(gotObj.Keys()) != 0 {
		t.Errorf("Compact({}) = %v, want empty object", d.Compact(emptyObj))
	}

	emptyArr := decode(t, `[]`)
	gotArr, ok := d.Compact(emptyArr).([]any)
	if !ok || len(gotArr) != 0 {
		t.Errorf("Compact([]) = %v, want empty []any", d.Compact(emptyArr))
	}
}

func TestWalkerArrayOfScalarsUnchanged(t *testing.T) {
	// An array of scalars -> compact declines -> unchanged after recursion.
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}
	arr := decode(t, `[1,2,3]`)
	got, ok := d.Compact(arr).([]any)
	if !ok || len(got) != 3 {
		t.Fatalf("Compact([1,2,3]) = %v, want 3-element []any", d.Compact(arr))
	}
	for i, want := range []string{"1", "2", "3"} {
		n, ok := got[i].(json.Number)
		if !ok || n.String() != want {
			t.Errorf("element %d = %v, want %s", i, got[i], want)
		}
	}
}

func TestWalkerPreservesKeyOrder(t *testing.T) {
	// walk_object builds a NEW object with the SAME key order.
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}}
	obj := decode(t, `{"z":1,"a":2,"m":3}`)
	got, ok := d.Compact(obj).(*orderedmap.OrderedMap)
	if !ok {
		t.Fatalf("Compact(object) = %T, want *orderedmap.OrderedMap", d.Compact(obj))
	}
	if keys := got.Keys(); strings.Join(keys, ",") != "z,a,m" {
		t.Errorf("key order = %v, want [z a m]", keys)
	}
}

func TestWalkerOpaqueBlobInWalkedValueRoundTrips(t *testing.T) {
	// A bulky opaque string inside a walked object -> comma-form
	// <<ccr:HASH,KIND,SIZE>> where HASH is the 24-hex BLAKE3 ccr.ComputeKey; the
	// store is written and CcrGet(hash) == payload byte-exact.
	store := newTestStore(t)
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}, CcrStore: store}

	blob := strings.Repeat("ABCDefgh1234+/wxYZ0987", 20) // >256, base64-like, high diversity
	obj := orderedmap.New()
	obj.Set("data", blob)

	got, ok := d.Compact(obj).(*orderedmap.OrderedMap)
	if !ok {
		t.Fatalf("Compact(object) = %T, want *orderedmap.OrderedMap", d.Compact(obj))
	}
	v, _ := got.Get("data")
	marker, ok := v.(string)
	if !ok || !strings.HasPrefix(marker, "<<ccr:") {
		t.Fatalf("walked opaque value = %v, want comma-form marker", v)
	}
	// Parse HASH out of <<ccr:HASH,KIND,SIZE>>.
	body := strings.TrimSuffix(strings.TrimPrefix(marker, "<<ccr:"), ">>")
	parts := strings.SplitN(body, ",", 2)
	hash := parts[0]
	// 24-hex BLAKE3.
	if len(hash) != 24 {
		t.Errorf("hash %q length = %d, want 24 (BLAKE3 ccr.ComputeKey)", hash, len(hash))
	}
	if hash != ccr.ComputeKey([]byte(blob)) {
		t.Errorf("hash = %q, want ccr.ComputeKey(blob) = %q", hash, ccr.ComputeKey([]byte(blob)))
	}
	// KIND is base64 for a base64-like blob.
	if !strings.HasPrefix(body, hash+",base64,") {
		t.Errorf("marker body = %q, want %q kind base64", body, hash)
	}
	// Round-trip: store.Get(hash) == blob byte-exact.
	got2, ok := store.Get(hash)
	if !ok || got2 != blob {
		t.Errorf("store.Get(%q) = (%q,%v), want blob byte-exact", hash, got2, ok)
	}
}

func TestWalkerNoStoreStillMarksButNoPut(t *testing.T) {
	// With a nil store the opaque blob is still replaced by a comma-form marker
	// (hash computed identically), but nothing is stored.
	d := DocumentCompactor{Config: DefaultCompactConfig(), Formatter: CsvSchemaFormatter{}} // CcrStore nil
	blob := strings.Repeat("ABCDefgh1234+/wxYZ0987", 20)
	obj := orderedmap.New()
	obj.Set("data", blob)
	got := d.Compact(obj).(*orderedmap.OrderedMap)
	v, _ := got.Get("data")
	marker, ok := v.(string)
	if !ok || !strings.HasPrefix(marker, "<<ccr:") {
		t.Errorf("nil-store opaque value = %v, want comma-form marker", v)
	}
}

func TestEmitOpaqueCCRMarkerHashAndStore(t *testing.T) {
	// EmitOpaqueCCRMarker: BLAKE3 24-hex ccr.ComputeKey + store.Put when non-nil.
	store := newTestStore(t)
	payload := "some-opaque-payload-bytes"
	m := EmitOpaqueCCRMarker(payload, LongString, store)
	wantHash := ccr.ComputeKey([]byte(payload))
	if !strings.HasPrefix(m, "<<ccr:"+wantHash+",string,") {
		t.Errorf("marker = %q, want prefix <<ccr:%s,string,", m, wantHash)
	}
	if got, ok := store.Get(wantHash); !ok || got != payload {
		t.Errorf("store.Get(%q) = (%q,%v), want %q", wantHash, got, ok, payload)
	}
	// Nil store: hash identical, no panic, no put.
	m2 := EmitOpaqueCCRMarker(payload, LongString, nil)
	if !strings.HasPrefix(m2, "<<ccr:"+wantHash+",string,") {
		t.Errorf("nil-store marker = %q, want same hash %s", m2, wantHash)
	}
}

func TestTryParseJSONContainer(t *testing.T) {
	// Only first-non-ws '{' or '[' AND parses to object/array. Bare scalars -> false.
	cases := []struct {
		in     string
		wantOk bool
	}{
		{`{"a":1}`, true},
		{`  [1,2]`, true},
		{`123`, false},
		{`true`, false},
		{`"str"`, false},
		{`{bad`, false},
		{``, false},
	}
	for _, tc := range cases {
		_, ok := TryParseJSONContainer(tc.in)
		if ok != tc.wantOk {
			t.Errorf("TryParseJSONContainer(%q) ok = %v, want %v", tc.in, ok, tc.wantOk)
		}
	}
}

func TestCompactDocumentConvenience(t *testing.T) {
	// CompactDocument uses default config + CSV-schema + no store; empty stays empty.
	obj := decode(t, `{}`)
	got, ok := CompactDocument(obj).(*orderedmap.OrderedMap)
	if !ok || len(got.Keys()) != 0 {
		t.Errorf("CompactDocument({}) = %v, want empty object", CompactDocument(obj))
	}
}
