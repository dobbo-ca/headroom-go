package tagprotect

import (
	"strings"
	"sync"
)

// html5AllowlistNames is the exact HTML5 element allowlist from upstream
// headroom (known_html_tag_names). Tags whose lowercased name is in this set
// are standard HTML5 elements and are NEVER protected (emitted verbatim).
// Order mirrors the upstream slice; membership is what matters.
var html5AllowlistNames = []string{
	// Document metadata
	"html", "base", "head", "link", "meta", "style", "title",
	// Sectioning root + content sectioning
	"body",
	"address", "article", "aside", "footer",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"header", "hgroup", "main", "nav", "section", "search",
	// Text content
	"blockquote", "dd", "div", "dl", "dt", "figcaption", "figure",
	"hr", "li", "menu", "ol", "p", "pre", "ul",
	// Inline text semantics
	"a", "abbr", "b", "bdi", "bdo", "br", "cite", "code", "data", "dfn",
	"em", "i", "kbd", "mark", "q", "rp", "rt", "ruby", "s", "samp",
	"small", "span", "strong", "sub", "sup", "time", "u", "var", "wbr",
	// Image and multimedia
	"area", "audio", "img", "map", "track", "video",
	// Embedded content
	"embed", "iframe", "object", "param", "picture", "portal", "source",
	"svg", "math",
	// Scripting
	"canvas", "noscript", "script",
	// Demarcating edits
	"del", "ins",
	// Table content
	"caption", "col", "colgroup", "table", "tbody", "td", "tfoot", "th",
	"thead", "tr",
	// Forms
	"button", "datalist", "fieldset", "form", "input", "label", "legend",
	"meter", "optgroup", "option", "output", "progress", "select", "textarea",
	// Interactive elements
	"details", "dialog", "summary",
	// Web components
	"slot", "template",
}

var (
	html5AllowlistOnce sync.Once
	html5Allowlist     map[string]struct{}
)

// knownHTMLTags builds the allowlist set once (OnceLock equivalent).
func knownHTMLTags() map[string]struct{} {
	html5AllowlistOnce.Do(func() {
		html5Allowlist = make(map[string]struct{}, len(html5AllowlistNames))
		for _, n := range html5AllowlistNames {
			html5Allowlist[n] = struct{}{}
		}
	})
	return html5Allowlist
}

// IsKnownHTMLTag reports whether name (lowercased first) is a standard HTML5
// element. Such tags are never protected — they are emitted verbatim.
func IsKnownHTMLTag(name string) bool {
	_, ok := knownHTMLTags()[strings.ToLower(name)]
	return ok
}
