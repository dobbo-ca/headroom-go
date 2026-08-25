package cachestab

import (
	"bytes"
	"strings"
	"testing"
)

// findingsAt returns the findings whose Location equals loc.
func findingsAt(fs []Finding, loc string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Location == loc {
			out = append(out, f)
		}
	}
	return out
}

func kinds(fs []Finding) []VolatileKind {
	out := make([]VolatileKind, len(fs))
	for i, f := range fs {
		out[i] = f.Kind
	}
	return out
}

func TestDetectVolatileTimestampShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"iso with T", "started at 2026-08-25T09:37:11Z", true},
		{"iso with space", "2026-08-25 09:37:11 INFO up", true},
		{"iso lowercase t", "2026-08-25t09:37:11", true},
		{"date only", "released on 2026-08-25 and that is all", false},
		{"letters where digits belong", "20X6-08-25T09:37:11", false},
		{"wrong separator", "2026/08/25T09:37:11", false},
		{"missing seconds", "2026-08-25T09:37", false},
		{"minute separator wrong", "2026-08-25T09-37-11", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"system":` + quote(tc.text) + `}`)
			got := len(findingsAt(DetectVolatile(body), "system")) > 0
			if got != tc.want {
				t.Errorf("detected=%v want=%v for %q", got, tc.want, tc.text)
			}
		})
	}
}

func TestDetectVolatileUUIDShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"uuid v4 variant a", "id 3f2504e0-4f89-41d3-9a0c-0305e82c3301", true},
		{"uuid v4 variant 8", "id 3f2504e0-4f89-41d3-8a0c-0305e82c3301", true},
		{"uuid v4 uppercase", "id 3F2504E0-4F89-41D3-BA0C-0305E82C3301", true},
		// The version nibble is what separates a per-request UUID from a
		// build hash. A v1 UUID is time-ordered but caller-stable.
		{"uuid v1 is not volatile", "id 3f2504e0-4f89-11d3-9a0c-0305e82c3301", false},
		{"bad variant nibble", "id 3f2504e0-4f89-41d3-ca0c-0305e82c3301", false},
		{"non-hex character", "id 3f2504e0-4f89-41d3-9a0c-0305e82c33zz", false},
		{"hyphens misplaced", "id 3f2504e04-f89-41d3-9a0c-0305e82c3301", false},
		{"too short", "id 3f2504e0-4f89-41d3-9a0c-0305e82c330", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"system":` + quote(tc.text) + `}`)
			got := len(findingsAt(DetectVolatile(body), "system")) > 0
			if got != tc.want {
				t.Errorf("detected=%v want=%v for %q", got, tc.want, tc.text)
			}
		})
	}
}

// A key named like a per-request identifier is volatile even when its value is
// an integer, which neither the timestamp nor the UUID scanner would catch.
func TestDetectVolatileIDNamedKeys(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result",` +
		`"trace_id":918273,"x_request_id":"abc","nested":{"correlation_id":"z"},` +
		`"declared_only":"","empty_obj_id":{"session_id":{}},"harmless":"value"}]}]}`)

	fs := DetectVolatile(body)
	var locs []string
	for _, f := range fs {
		if f.Kind == KindIDField {
			locs = append(locs, f.Location)
		}
	}
	joined := strings.Join(locs, " ")
	for _, want := range []string{
		"messages[0].content[0].trace_id",
		"messages[0].content[0].x_request_id",
		"messages[0].content[0].nested.correlation_id",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing id-field finding at %s; got %v", want, locs)
		}
	}
	// A schema that merely DECLARES an id field, with no value, is not
	// volatile: nothing changes between requests.
	for _, notWant := range []string{"declared_only", "empty_obj_id.session_id"} {
		if strings.Contains(joined, notWant) {
			t.Errorf("empty value at %s reported as volatile; got %v", notWant, locs)
		}
	}
}

// Location paths must name the exact field, or the warning cannot be acted on.
func TestDetectVolatileLocationPaths(t *testing.T) {
	body := []byte(`{` +
		`"system":[{"type":"text","text":"at 2026-08-25T09:37:11Z"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"x"},` +
		`{"type":"text","text":"at 2026-08-25T09:37:11Z"}]}]}`)

	var got []string
	for _, f := range DetectVolatile(body) {
		got = append(got, f.Location)
	}
	for _, want := range []string{
		"system[0].text",
		"messages[0].content[1].text",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no finding at %q; got %v", want, got)
		}
	}
}

// A noisy payload must not drown the log.
func TestDetectVolatileCapsFindings(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("2026-08-25T09:37:11Z ")
	}
	body := []byte(`{"system":` + quote(sb.String()) + `}`)

	fs := DetectVolatile(body)
	if len(fs) != MaxFindings {
		t.Errorf("got %d findings, want the cap of %d", len(fs), MaxFindings)
	}
}

func TestDetectVolatileTruncatesLongSamples(t *testing.T) {
	long := strings.Repeat("é", 300) // multi-byte, so a naive cut splits a rune
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text",` +
		`"trace_id":` + quote(long) + `}]}]}`)

	fs := DetectVolatile(body)
	if len(fs) == 0 {
		t.Fatal("no finding for a long id-field value")
	}
	s := fs[0].Sample
	if len(s) > SampleBytes+len("…") {
		t.Errorf("sample is %d bytes, want at most %d", len(s), SampleBytes+len("…"))
	}
	if !utf8Valid(s) {
		t.Error("sample is not valid UTF-8; a rune was split")
	}
}

// The whole package is read-only. Nothing may alter the caller's buffer.
func TestDetectVolatileNeverMutatesInput(t *testing.T) {
	body := []byte(`{"system":"at 2026-08-25T09:37:11Z and 3f2504e0-4f89-41d3-9a0c-0305e82c3301",` +
		`"messages":[{"role":"user","content":"x"}],"tools":[{"input_schema":{"trace_id":1}}]}`)
	before := append([]byte(nil), body...)

	if len(DetectVolatile(body)) == 0 {
		t.Fatal("fixture produced no findings; it cannot prove non-mutation on the scanning path")
	}
	if !bytes.Equal(body, before) {
		t.Error("DetectVolatile altered the caller's buffer")
	}
}

func TestDetectVolatileIgnoresNonObjectBodies(t *testing.T) {
	for _, body := range []string{`[]`, `"a string"`, `not json at all`, ``} {
		if fs := DetectVolatile([]byte(body)); fs != nil {
			t.Errorf("body %q produced %d findings, want none", body, len(fs))
		}
	}
}

// Multiple distinct volatile items in one string each get reported, so a
// customer sees every offender rather than only the first.
func TestDetectVolatileReportsEveryItemInOneString(t *testing.T) {
	body := []byte(`{"system":"2026-08-25T09:37:11Z req 3f2504e0-4f89-41d3-9a0c-0305e82c3301"}`)
	got := kinds(DetectVolatile(body))
	if len(got) != 2 {
		t.Fatalf("got %d findings %v, want a timestamp and a uuid", len(got), got)
	}
	if got[0] != KindTimestamp || got[1] != KindUUID {
		t.Errorf("kinds = %v, want [%s %s]", got, KindTimestamp, KindUUID)
	}
}

func quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// Tool definitions are deliberately not scanned. A tool schema is static by
// construction, so its ISO-8601 examples and its id-named schema PROPERTIES
// are byte-identical every request and cannot bust a cache. Measured against
// real Claude Code traffic, scanning them consumed 6 of the 10 finding slots
// and crowded out 4 genuine per-request UUIDs.
//
// A tools array that really does change between turns is reported by the E6
// drift detector's tools axis instead, from observed change rather than
// guessed shape.
func TestDetectVolatileIgnoresToolDefinitions(t *testing.T) {
	// Every shape that WOULD match, sitting in a tool definition.
	body := []byte(`{"tools":[{"name":"t","description":"e.g. 2026-03-09T09:00:00.000+0000",` +
		`"input_schema":{"type":"object","properties":{` +
		`"session_id":{"type":"string","description":"a run session id"},` +
		`"request_id":{"type":"string"},` +
		`"started":{"description":"when, e.g. 2026-03-09T09:00:00"},` +
		`"uuid":{"description":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}}}}]}`)

	if fs := DetectVolatile(body); len(fs) != 0 {
		t.Errorf("tool definitions produced %d findings, want none: %+v", len(fs), fs)
	}

	// The same shapes in a MESSAGE are real findings, so this test cannot
	// pass just because the scanner stopped working.
	inMessage := []byte(`{"messages":[{"role":"user","content":[{"type":"text",` +
		`"text":"at 2026-03-09T09:00:00 id 3f2504e0-4f89-41d3-9a0c-0305e82c3301"}]}]}`)
	if fs := DetectVolatile(inMessage); len(fs) != 2 {
		t.Errorf("the same shapes in a message produced %d findings, want 2: %+v", len(fs), fs)
	}
}
