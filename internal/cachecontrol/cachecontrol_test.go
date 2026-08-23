package cachecontrol

import "testing"

func TestComputeFrozenCount(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "no markers anywhere yields zero",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			want: 0,
		},
		{
			// The floor is exclusive: a marker on index 0 freezes index 0,
			// so the first live index is 1.
			name: "marker on index 0 yields one",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"text","text":"b"}]}]}`,
			want: 1,
		},
		{
			name: "marker on index 2 of four yields three",
			body: `{"messages":[
				{"role":"user","content":[{"type":"text","text":"a"}]},
				{"role":"assistant","content":[{"type":"text","text":"b"}]},
				{"role":"user","content":[{"type":"text","text":"c","cache_control":{"type":"ephemeral"}}]},
				{"role":"assistant","content":[{"type":"text","text":"d"}]}]}`,
			want: 3,
		},
		{
			// The HIGHEST marker index wins, not the first or the last seen.
			name: "highest marker index wins",
			body: `{"messages":[
				{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},
				{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]},
				{"role":"user","content":[{"type":"text","text":"c"}]}]}`,
			want: 2,
		},
		{
			// A marker in a later block of the same message must not
			// double-count.
			name: "two markers in one message still yield index plus one",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}]}`,
			want: 1,
		},
		{
			// system markers are cache-hot unconditionally and must NOT
			// raise the message floor.
			name: "system block marker does not raise the floor",
			body: `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			want: 0,
		},
		{
			name: "tools marker does not raise the floor",
			body: `{"tools":[{"name":"t","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			want: 0,
		},
		{
			// A string system field cannot carry a marker at all.
			name: "string system field is tolerated",
			body: `{"system":"you are helpful","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			want: 0,
		},
		{
			// Anthropic's legacy shape: content is a plain JSON string, so
			// there is no block list and no marker is possible.
			name: "string message content has no blocks",
			body: `{"messages":[{"role":"user","content":"plain string"}]}`,
			want: 0,
		},
		{
			name: "missing messages field yields zero",
			body: `{"model":"claude-3"}`,
			want: 0,
		},
		{
			name: "messages not an array yields zero",
			body: `{"messages":"oops"}`,
			want: 0,
		},
		{
			name: "invalid json yields zero",
			body: `{not json`,
			want: 0,
		},
		{
			name: "empty body yields zero",
			body: ``,
			want: 0,
		},
		{
			name: "empty messages array yields zero",
			body: `{"messages":[]}`,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ComputeFrozenCount([]byte(tt.body))
			if got != tt.want {
				t.Errorf("ComputeFrozenCount = %d, want %d", got, tt.want)
			}
		})
	}
}

// The floor must equal the marker index plus one, checked across a range so a
// constant-shift off-by-one cannot pass by coincidence on a single case.
func TestFrozenCountIsMarkerIndexPlusOne(t *testing.T) {
	for markerAt := 0; markerAt < 5; markerAt++ {
		body := `{"messages":[`
		for i := 0; i < 5; i++ {
			if i > 0 {
				body += ","
			}
			body += `{"role":"user","content":[{"type":"text","text":"m"`
			if i == markerAt {
				body += `,"cache_control":{"type":"ephemeral"}`
			}
			body += `}]}`
		}
		body += `]}`

		got, _ := ComputeFrozenCount([]byte(body))
		if want := markerAt + 1; got != want {
			t.Errorf("marker at index %d: got frozen count %d, want %d", markerAt, got, want)
		}
	}
}

// A 5m marker preceding a 1h marker violates the Anthropic ordering rule. We
// warn and still return the correct count — never reject.
func TestTTLOrderingWarnsButDoesNotChangeCount(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"5m"}}]},
		{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`

	got, warns := ComputeFrozenCount([]byte(body))
	if got != 2 {
		t.Errorf("frozen count = %d, want 2 despite the ordering violation", got)
	}
	if len(warns) == 0 {
		t.Fatal("expected a TTL-ordering warning, got none")
	}
	if warns[0].Field != "messages" {
		t.Errorf("warning Field = %q, want %q", warns[0].Field, "messages")
	}
}

func TestTTLOrderingCorrectOrderDoesNotWarn(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]},
		{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`

	got, warns := ComputeFrozenCount([]byte(body))
	if got != 2 {
		t.Errorf("frozen count = %d, want 2", got)
	}
	if len(warns) != 0 {
		t.Errorf("expected no warnings for correct 1h-before-5m order, got %+v", warns)
	}
}

// Markers with no ttl field default to the 5m lane and must not spuriously warn.
func TestMarkersWithoutTTLDoNotWarn(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},
		{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}]}`

	if _, warns := ComputeFrozenCount([]byte(body)); len(warns) != 0 {
		t.Errorf("expected no warnings when no ttl is set, got %+v", warns)
	}
}

// ComputeFrozenCount must never panic on hostile input.
func TestComputeFrozenCountNeverPanics(t *testing.T) {
	bodies := []string{
		``, `null`, `[]`, `"str"`, `{"messages":null}`,
		`{"messages":[null]}`,
		`{"messages":[{"content":null}]}`,
		`{"messages":[{"content":[null]}]}`,
		`{"messages":[{"content":[{"cache_control":"not-an-object"}]}]}`,
		`{"system":null,"tools":null,"messages":[]}`,
		`{"tools":[null],"messages":[]}`,
	}
	for _, b := range bodies {
		t.Run(b, func(t *testing.T) {
			if got, _ := ComputeFrozenCount([]byte(b)); got != 0 {
				t.Errorf("ComputeFrozenCount(%q) = %d, want 0", b, got)
			}
		})
	}
}

// The messages warning must carry the index of the offending message, not a
// placeholder, so a caller can point the customer at the block to move.
func TestMessagesWarningCarriesMessageIndex(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"5m"}}]},
		{"role":"user","content":[{"type":"text","text":"b"}]},
		{"role":"user","content":[{"type":"text","text":"c","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`

	_, warns := ComputeFrozenCount([]byte(body))
	if len(warns) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(warns), warns)
	}
	if warns[0].Index != 2 {
		t.Errorf("warning Index = %d, want 2 (the index of the 1h marker)", warns[0].Index)
	}
}

// system and tools are walked for TTL ordering too. They never raise the
// floor, but a 5m-before-1h marker there is still worth a warning.
func TestTTLOrderingInSystemAndTools(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantField string
	}{
		{
			name: "system out of order warns",
			body: `{"system":[
				{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"5m"}},
				{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":"1h"}}],
				"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			wantField: "system",
		},
		{
			name: "tools out of order warns",
			body: `{"tools":[
				{"name":"t1","cache_control":{"type":"ephemeral","ttl":"5m"}},
				{"name":"t2","cache_control":{"type":"ephemeral","ttl":"1h"}}],
				"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
			wantField: "tools",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warns := ComputeFrozenCount([]byte(tt.body))
			if got != 0 {
				t.Errorf("frozen count = %d, want 0: %s markers never raise the floor", got, tt.wantField)
			}
			if len(warns) != 1 {
				t.Fatalf("got %d warnings, want 1: %+v", len(warns), warns)
			}
			if warns[0].Field != tt.wantField {
				t.Errorf("warning Field = %q, want %q", warns[0].Field, tt.wantField)
			}
			if warns[0].Index != -1 {
				t.Errorf("warning Index = %d, want -1 for a single-list field", warns[0].Index)
			}
			if warns[0].Msg == "" {
				t.Error("warning Msg is empty")
			}
		})
	}
}

// Correctly ordered system and tools markers must stay silent.
func TestTTLOrderingInSystemAndToolsCorrectOrderDoesNotWarn(t *testing.T) {
	body := `{"system":[
		{"type":"text","text":"s1","cache_control":{"type":"ephemeral","ttl":"1h"}},
		{"type":"text","text":"s2","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"tools":[
		{"name":"t1","cache_control":{"type":"ephemeral","ttl":"1h"}},
		{"name":"t2","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`

	if _, warns := ComputeFrozenCount([]byte(body)); len(warns) != 0 {
		t.Errorf("expected no warnings for correct 1h-before-5m order, got %+v", warns)
	}
}

// A 5m marker in system must not leak into the tools walk: the ordering rule
// is per field.
func TestTTLOrderingIsPerField(t *testing.T) {
	body := `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"tools":[{"name":"t","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`

	if _, warns := ComputeFrozenCount([]byte(body)); len(warns) != 0 {
		t.Errorf("a 5m marker in system must not warn about a 1h marker in another field, got %+v", warns)
	}
}

// Content must be a block list. An object content is malformed Anthropic and
// must not be walked as a single block, or a stray cache_control key would
// freeze the message.
func TestObjectMessageContentHasNoBlocks(t *testing.T) {
	body := `{"messages":[{"role":"user","content":{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}}]}`
	if got, _ := ComputeFrozenCount([]byte(body)); got != 0 {
		t.Errorf("ComputeFrozenCount = %d, want 0", got)
	}
}
