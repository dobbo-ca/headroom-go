package smartcrusher

// ErrorKeywords is the fallback error-preservation signal: an item whose
// lowercased JSON contains any of these substrings is kept. The list is
// lowercase by construction; the CALLER lowercases the haystack and does plain
// substring containment (not whole-word/prefix/regex), biasing toward
// over-preservation. The rustdoc inside constraints.rs wrongly lists 11
// (omitting "failure"); the authoritative count is 12 [ref: error_keywords.rs].
var ErrorKeywords = []string{
	"error", "exception", "failed", "failure", "critical", "fatal",
	"crash", "panic", "abort", "timeout", "denied", "rejected",
}
