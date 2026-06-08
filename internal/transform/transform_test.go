package transform

import (
	"errors"
	"testing"
)

func TestContentTypeString(t *testing.T) {
	cases := map[ContentType]string{
		JsonArray:     "json_array",
		SourceCode:    "source_code",
		SearchResults: "search",
		BuildOutput:   "build",
		GitDiff:       "diff",
		Html:          "html",
		PlainText:     "text",
	}
	for ct, want := range cases {
		if got := ct.String(); got != want {
			t.Errorf("ContentType(%d).String() = %q, want %q", ct, got, want)
		}
	}
}

func TestContentTypeStringUnknown(t *testing.T) {
	if got := ContentType(99).String(); got != "text" {
		t.Errorf("unknown ContentType should fall back to %q, got %q", "text", got)
	}
}

func TestSentinelErrorsDistinct(t *testing.T) {
	if errors.Is(ErrSkipped, ErrInvalidInput) || errors.Is(ErrInternal, ErrSkipped) {
		t.Fatal("sentinel errors must be distinct")
	}
}
