package ccr

import "testing"

func TestCanonicalMarkerRoundTrip(t *testing.T) {
	h := "0123456789abcdef01234567"
	m := MarkerFor(h)
	if m != "<<ccr:"+h+">>" {
		t.Fatalf("MarkerFor = %q", m)
	}
	got, ok := ParseMarker(m)
	if !ok || got != h {
		t.Fatalf("ParseMarker(%q) = %q,%v", m, got, ok)
	}
}

func TestParseMarkerRejectsNonMarker(t *testing.T) {
	if _, ok := ParseMarker("not a marker"); ok {
		t.Fatal("ParseMarker accepted non-marker text")
	}
	if _, ok := ParseMarker("<<ccr:short>>"); ok {
		t.Fatal("ParseMarker accepted wrong-length hash")
	}
	h := "0123456789abcdef01234567"
	if _, ok := ParseMarker(MarkerForCell(h, "json", 42)); ok {
		t.Fatal("ParseMarker accepted cell marker")
	}
	if _, ok := ParseMarker(MarkerForLossy(h, 3)); ok {
		t.Fatal("ParseMarker accepted lossy marker")
	}
}

func TestCellAndLossyMarkersDistinct(t *testing.T) {
	h := "0123456789abcdef01234567"
	if MarkerForCell(h, "json", 42) != "<<ccr:"+h+",json,42>>" {
		t.Fatalf("cell marker = %q", MarkerForCell(h, "json", 42))
	}
	if MarkerForLossy(h, 7) != "<<ccr:"+h+" 7_rows_offloaded>>" {
		t.Fatalf("lossy marker = %q", MarkerForLossy(h, 7))
	}
}
