// Package cachecontrol walks the customer's cache_control markers in an
// Anthropic /v1/messages request body and computes the frozen-message floor
// below which the live-zone dispatcher must not touch a byte.
//
// Anthropic prompt caching pins a prefix of the request: every block up to
// and including the last cache_control marker is part of the cache key.
// Modifying any byte of that prefix changes the key, drops the hit rate to
// zero, and silently inflates the customer's bill.
package cachecontrol

import (
	"log/slog"

	"github.com/tidwall/gjson"
)

const (
	// TTL1H selects the hour-long ephemeral cache lane.
	TTL1H = "1h"
	// TTL5M selects the five-minute lane, which is the default.
	TTL5M = "5m"
)

// Warning records an advisory the walker found. It never blocks a request.
type Warning struct {
	// Field is "messages", "system", or "tools".
	Field string
	// Index is the message index for "messages", or -1 for "system" and
	// "tools" where the field is a single list.
	Index int
	Msg   string
}

// ComputeFrozenCount returns the smallest N such that messages[i] is in the
// cache hot zone for every i < N, together with any advisory warnings.
//
// For each marker in messages[i].content[*].cache_control the floor rises to
// at least i+1. The +1 is exclusive: messages[i] itself is part of the
// cached prefix, so the dispatcher must not touch any index up to and
// including i.
//
// Markers in system and tools[*] are checked for TTL ordering but do NOT
// raise the message floor — those fields are unconditionally cache-hot, so
// marker placement there cannot make an earlier message live.
//
// Returns 0 when the body is not JSON, has no messages array, or carries no
// marker in messages[*].
func ComputeFrozenCount(body []byte) (int, []Warning) {
	var warns []Warning

	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return 0, nil
	}

	highest := -1
	messages := root.Get("messages")
	if messages.IsArray() {
		// seen5m tracks TTL ordering across the whole messages field, not
		// per message: the "same field" the ordering rule refers to is
		// "messages" as a whole, so a 5m marker in an earlier message must
		// still be visible when a later message's 1h marker is checked.
		seen5m := false
		for i, msg := range messages.Array() {
			blocks := msg.Get("content")
			if !blocks.IsArray() {
				// String content (Anthropic legacy shape) carries no block
				// list, so it cannot carry a marker.
				continue
			}
			for _, block := range blocks.Array() {
				marker := block.Get("cache_control")
				if !marker.IsObject() {
					continue
				}
				if i > highest {
					highest = i
				}
				slog.Debug("cache_control marker",
					"field", "messages", "index", i, "ttl", marker.Get("ttl").String())
				if w, ok := checkTTLOrder(marker, &seen5m, "messages", i); ok {
					warns = append(warns, w)
				}
			}
		}
	}

	warns = append(warns, walkList(root.Get("system"), "system")...)
	warns = append(warns, walkList(root.Get("tools"), "tools")...)

	if highest < 0 {
		return 0, warns
	}
	return highest + 1, warns
}

// walkList checks a system or tools block list for TTL ordering. These
// markers never raise the message floor.
func walkList(list gjson.Result, field string) []Warning {
	if !list.IsArray() {
		return nil
	}
	var warns []Warning
	seen5m := false
	for _, block := range list.Array() {
		marker := block.Get("cache_control")
		if !marker.IsObject() {
			continue
		}
		slog.Debug("cache_control marker",
			"field", field, "ttl", marker.Get("ttl").String())
		if w, ok := checkTTLOrder(marker, &seen5m, field, -1); ok {
			warns = append(warns, w)
		}
	}
	return warns
}

// checkTTLOrder reports a violation of the Anthropic rule that 1h markers
// must precede 5m markers within a field. seen5m tracks whether a 5m marker
// already appeared in this field's walk.
func checkTTLOrder(marker gjson.Result, seen5m *bool, field string, index int) (Warning, bool) {
	switch marker.Get("ttl").String() {
	case TTL5M:
		*seen5m = true
	case TTL1H:
		if *seen5m {
			w := Warning{
				Field: field,
				Index: index,
				Msg:   "cache_control ttl 1h follows 5m; Anthropic expects 1h markers first",
			}
			slog.Warn(w.Msg, "field", field, "index", index)
			return w, true
		}
	}
	return Warning{}, false
}
