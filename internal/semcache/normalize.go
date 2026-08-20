// Package semcache returns a stored provider response when a new request
// embeds close to one already seen. It is opt-in and lossy by design: a hit
// answers a question with the answer to a similar-but-different question.
package semcache

import "strings"

// Normalize reduces a request to the text that gets embedded. Two requests
// that differ only in whitespace or letter case must produce one string, so
// that trivial formatting differences do not defeat a cache hit.
//
// This never touches the bytes sent upstream. On a miss the original request
// is forwarded verbatim, so the byte-surgery invariant (I1) is untouched.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
