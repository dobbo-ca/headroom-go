// Package headers filters HTTP headers across the proxy hop. Hop-by-hop
// headers (RFC 7230 6.1) must not cross a proxy; headroom's own x-headroom-*
// headers must not leak upstream; and the client's address is appended to
// X-Forwarded-For.
package headers

import (
	"net"
	"net/http"
	"strings"
)

// InternalHeaderPrefix marks headroom's own headers. They describe the
// proxy's work to the client and must never cross the upstream boundary.
const InternalHeaderPrefix = "x-headroom-"

// hopByHop must be stripped at every hop (RFC 7230 6.1).
var hopByHop = []string{
	"connection",
	"keep-alive",
	"proxy-authenticate",
	"proxy-authorization",
	"te",
	"trailers",
	"transfer-encoding",
	"upgrade",
}

// clientManaged are set by the outgoing HTTP client itself; copying the
// client's values would send the wrong Host and a stale Content-Length.
var clientManaged = []string{"host", "content-length"}

func matches(list []string, name string) bool {
	n := strings.ToLower(name)
	for _, h := range list {
		if h == n {
			return true
		}
	}
	return false
}

// IsHopByHop reports whether name is hop-by-hop.
func IsHopByHop(name string) bool { return matches(hopByHop, name) }

// IsRequestDrop reports whether name must not be forwarded upstream.
func IsRequestDrop(name string) bool { return IsHopByHop(name) || matches(clientManaged, name) }

// IsResponseDrop reports whether name must not be copied back to the client.
// Content-Length is deliberately kept so clients see the real body length.
func IsResponseDrop(name string) bool { return IsHopByHop(name) }

// IsInternal reports whether name is one of headroom's own headers.
func IsInternal(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), InternalHeaderPrefix)
}

// ConnectionListed returns the lowercased header names named inside
// Connection. Those are hop-by-hop for this hop even though they are not on
// the static list.
func ConnectionListed(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Connection") {
		for _, part := range strings.Split(v, ",") {
			if s := strings.ToLower(strings.TrimSpace(part)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// AppendXFF appends remoteAddr's IP to X-Forwarded-For, or sets it.
func AppendXFF(h http.Header, remoteAddr string) {
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}
	if ip == "" {
		return
	}
	if prior := h.Get("X-Forwarded-For"); prior != "" {
		h.Set("X-Forwarded-For", prior+", "+ip)
		return
	}
	h.Set("X-Forwarded-For", ip)
}

// BuildForwardRequest returns the header set to send upstream. It never
// mutates or aliases src.
func BuildForwardRequest(src http.Header) http.Header {
	listed := map[string]bool{}
	for _, n := range ConnectionListed(src) {
		listed[n] = true
	}

	out := make(http.Header, len(src))
	for name, values := range src {
		if IsRequestDrop(name) || IsInternal(name) || listed[strings.ToLower(name)] {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}

// FilterResponse returns the header set to copy back to the client.
func FilterResponse(src http.Header) http.Header {
	listed := map[string]bool{}
	for _, n := range ConnectionListed(src) {
		listed[n] = true
	}

	out := make(http.Header, len(src))
	for name, values := range src {
		if IsResponseDrop(name) || listed[strings.ToLower(name)] {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}
