package headers

import (
	"net/http"
	"testing"
)

func TestIsHopByHop(t *testing.T) {
	for _, n := range []string{
		"Connection", "keep-alive", "Proxy-Authenticate", "proxy-authorization",
		"TE", "Trailers", "Transfer-Encoding", "Upgrade",
	} {
		if !IsHopByHop(n) {
			t.Errorf("IsHopByHop(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"Authorization", "Content-Type", "X-Api-Key", "User-Agent", ""} {
		if IsHopByHop(n) {
			t.Errorf("IsHopByHop(%q) = true, want false", n)
		}
	}
}

func TestIsRequestDropAddsClientManaged(t *testing.T) {
	for _, n := range []string{"Host", "content-length", "Connection"} {
		if !IsRequestDrop(n) {
			t.Errorf("IsRequestDrop(%q) = false, want true", n)
		}
	}
	// Authorization must survive: dropping it breaks every upstream call.
	if IsRequestDrop("Authorization") {
		t.Error("Authorization must be forwarded")
	}
}

// Content-Length must survive on the response side so clients see the length.
func TestIsResponseDropKeepsContentLength(t *testing.T) {
	if IsResponseDrop("Content-Length") {
		t.Error("Content-Length must be kept on the response side")
	}
	if !IsResponseDrop("Transfer-Encoding") {
		t.Error("Transfer-Encoding must be dropped on the response side")
	}
}

func TestIsInternal(t *testing.T) {
	for _, n := range []string{"X-Headroom-Tokens-Saved", "x-headroom-x", "X-HEADROOM-Y"} {
		if !IsInternal(n) {
			t.Errorf("IsInternal(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"X-Headroomish", "headroom", "X-Forwarded-For"} {
		if IsInternal(n) {
			t.Errorf("IsInternal(%q) = true, want false", n)
		}
	}
}

func TestConnectionListed(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "keep-alive, X-Custom-Hop , ")
	got := ConnectionListed(h)
	want := map[string]bool{"keep-alive": true, "x-custom-hop": true}
	if len(got) != 2 {
		t.Fatalf("ConnectionListed = %v, want 2 entries", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected entry %q", g)
		}
	}
}

// A header named in Connection is hop-by-hop for this hop and must not be
// forwarded, even though it is not on the static list.
func TestBuildForwardRequestDropsConnectionListed(t *testing.T) {
	src := http.Header{}
	src.Set("Connection", "X-Custom-Hop")
	src.Set("X-Custom-Hop", "secret")
	src.Set("Authorization", "Bearer sk-ant-api03-x")

	out := BuildForwardRequest(src)
	if out.Get("X-Custom-Hop") != "" {
		t.Error("a Connection-listed header was forwarded")
	}
	if out.Get("Connection") != "" {
		t.Error("Connection itself was forwarded")
	}
	if out.Get("Authorization") != "Bearer sk-ant-api03-x" {
		t.Error("Authorization was not forwarded")
	}
}

func TestBuildForwardRequestDropsInternalAndClientManaged(t *testing.T) {
	src := http.Header{}
	src.Set("X-Headroom-Debug", "1")
	src.Set("Host", "evil.example")
	src.Set("Content-Length", "999")
	src.Set("Content-Type", "application/json")

	out := BuildForwardRequest(src)
	for _, n := range []string{"X-Headroom-Debug", "Host", "Content-Length"} {
		if out.Get(n) != "" {
			t.Errorf("%s must not be forwarded", n)
		}
	}
	if out.Get("Content-Type") != "application/json" {
		t.Error("Content-Type must be forwarded")
	}
}

// Multi-valued headers must survive intact; a naive Get/Set copy loses values.
func TestBuildForwardRequestPreservesMultipleValues(t *testing.T) {
	src := http.Header{}
	src.Add("X-Multi", "a")
	src.Add("X-Multi", "b")

	out := BuildForwardRequest(src)
	if got := out.Values("X-Multi"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("X-Multi = %v, want [a b]", got)
	}
}

func TestAppendXFF(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		remote   string
		want     string
	}{
		{"sets when absent", "", "203.0.113.7:54321", "203.0.113.7"},
		{"appends when present", "198.51.100.1", "203.0.113.7:54321", "198.51.100.1, 203.0.113.7"},
		{"host without port is used as-is", "", "203.0.113.7", "203.0.113.7"},
		{"ipv6 with port", "", "[2001:db8::1]:443", "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.existing != "" {
				h.Set("X-Forwarded-For", tt.existing)
			}
			AppendXFF(h, tt.remote)
			if got := h.Get("X-Forwarded-For"); got != tt.want {
				t.Errorf("X-Forwarded-For = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterResponse(t *testing.T) {
	src := http.Header{}
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Content-Length", "42")
	src.Set("Content-Type", "text/event-stream")

	out := FilterResponse(src)
	if out.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding must be dropped")
	}
	if out.Get("Content-Length") != "42" {
		t.Error("Content-Length must be kept")
	}
	if out.Get("Content-Type") != "text/event-stream" {
		t.Error("Content-Type must be kept")
	}
}

// BuildForwardRequest must not alias the caller's header map.
func TestBuildForwardRequestDoesNotAliasSource(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	out := BuildForwardRequest(src)
	out.Set("Content-Type", "text/plain")
	if src.Get("Content-Type") != "application/json" {
		t.Error("BuildForwardRequest mutated its input")
	}
}

// A name that merely starts with a hop-by-hop name is a distinct header.
// Upgrade-Insecure-Requests is a real browser header, not an Upgrade.
func TestDropListsMatchWholeNamesOnly(t *testing.T) {
	for _, n := range []string{"Upgrade-Insecure-Requests", "Team-Id", "Connection-Id"} {
		if IsHopByHop(n) {
			t.Errorf("IsHopByHop(%q) = true, want false", n)
		}
	}
	for _, n := range []string{"Hostname", "Content-Length-Hint"} {
		if IsRequestDrop(n) {
			t.Errorf("IsRequestDrop(%q) = true, want false", n)
		}
	}
}

// The prefix must anchor: an embedded x-headroom- is somebody else's header.
func TestIsInternalRequiresPrefixNotSubstring(t *testing.T) {
	if IsInternal("X-Foo-x-headroom-bar") {
		t.Error("IsInternal matched an embedded prefix; it must anchor at the start")
	}
}

// Connection may arrive as repeated header lines; every line counts.
func TestConnectionListedAcrossRepeatedLines(t *testing.T) {
	h := http.Header{}
	h.Add("Connection", "keep-alive")
	h.Add("Connection", "X-Second-Hop")

	got := ConnectionListed(h)
	if len(got) != 2 || got[0] != "keep-alive" || got[1] != "x-second-hop" {
		t.Fatalf("ConnectionListed = %v, want [keep-alive x-second-hop]", got)
	}

	src := http.Header{}
	src.Add("Connection", "keep-alive")
	src.Add("Connection", "X-Second-Hop")
	src.Set("X-Second-Hop", "secret")
	if BuildForwardRequest(src).Get("X-Second-Hop") != "" {
		t.Error("a header listed on a second Connection line was forwarded")
	}
}

// Upstream may declare its own hop-by-hop headers in Connection; those are
// for this hop only and must not reach the client.
func TestFilterResponseDropsConnectionListed(t *testing.T) {
	src := http.Header{}
	src.Set("Connection", "X-Upstream-Hop")
	src.Set("X-Upstream-Hop", "internal")
	src.Set("Content-Type", "text/event-stream")

	out := FilterResponse(src)
	if out.Get("X-Upstream-Hop") != "" {
		t.Error("a Connection-listed response header was copied to the client")
	}
	if out.Get("Content-Type") != "text/event-stream" {
		t.Error("Content-Type must be kept")
	}
}

// The copy must be deep: writing through a returned value slot must not
// reach back into the caller's header map.
func TestBuildForwardRequestDoesNotAliasValueSlices(t *testing.T) {
	src := http.Header{}
	src.Add("X-Multi", "a")
	src.Add("X-Multi", "b")

	out := BuildForwardRequest(src)
	out.Values("X-Multi")[0] = "clobbered"
	if got := src.Values("X-Multi")[0]; got != "a" {
		t.Errorf("src X-Multi[0] = %q, want %q; value slice was aliased", got, "a")
	}
}
