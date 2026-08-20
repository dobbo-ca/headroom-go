package compress

import "testing"

func TestStripANSIRemovesColorCodes(t *testing.T) {
	in := "\x1b[31merror\x1b[0m: failed"
	if got, want := stripANSI(in), "error: failed"; got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

func TestStripANSILeavesPlainTextAlone(t *testing.T) {
	in := "error: failed"
	if got := stripANSI(in); got != in {
		t.Errorf("stripANSI = %q, want unchanged %q", got, in)
	}
}

func TestStripANSIHandlesMultiParameterCodes(t *testing.T) {
	in := "\x1b[1;33;40mwarn\x1b[0m"
	if got, want := stripANSI(in), "warn"; got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

func TestCollapseRunsFoldsIdenticalLines(t *testing.T) {
	in := "a\nb\nb\nb\nc"
	want := "a\nb\n... previous line repeated 2 more times\nc"
	if got := collapseRuns(in); got != want {
		t.Errorf("collapseRuns = %q, want %q", got, want)
	}
}

func TestCollapseRunsLeavesSingleLinesAlone(t *testing.T) {
	in := "a\nb\nc"
	if got := collapseRuns(in); got != in {
		t.Errorf("collapseRuns = %q, want unchanged %q", got, in)
	}
}

func TestCollapseRunsHandlesRunAtEnd(t *testing.T) {
	in := "a\nz\nz"
	want := "a\nz\n... previous line repeated 1 more times"
	if got := collapseRuns(in); got != want {
		t.Errorf("collapseRuns = %q, want %q", got, want)
	}
}

func TestDedupWarningsKeepsFirstDropsRepeats(t *testing.T) {
	in := "warning: unused var x\nok\nwarning: unused var x\nwarning: unused var x"
	got := dedupWarnings(in)
	if want := "warning: unused var x\nok\n... 2 more occurrences of 1 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsKeepsDistinctWarnings(t *testing.T) {
	in := "warning: a\nwarning: b"
	if got := dedupWarnings(in); got != in {
		t.Errorf("dedupWarnings = %q, want unchanged %q", got, in)
	}
}

func TestDedupWarningsIsCaseInsensitiveOnTheMarker(t *testing.T) {
	in := "WARNING: dup\nWARNING: dup"
	got := dedupWarnings(in)
	if want := "WARNING: dup\n... 1 more occurrences of 1 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsCountsMultipleDistinctDuplicates(t *testing.T) {
	in := "warning: a\nwarning: b\nwarning: a\nwarning: b\nwarning: c"
	got := dedupWarnings(in)
	if want := "warning: a\nwarning: b\nwarning: c\n... 2 more occurrences of 2 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsMatchesWarnMarker(t *testing.T) {
	in := "warn: dup\nwarn: dup"
	got := dedupWarnings(in)
	if want := "warn: dup\n... 1 more occurrences of 1 duplicated warning"; got != want {
		t.Errorf("dedupWarnings = %q, want %q", got, want)
	}
}

func TestDedupWarningsHandlesNonASCIIBeforeMarker(t *testing.T) {
	// \x89 is invalid UTF-8; strings.ToLower would expand it to a
	// multi-byte replacement and desync the marker index from the
	// original line, which used to panic on a slice out of range.
	// The marker-with-no-body form pushes the desynced index past the
	// end of the line, which is what actually trips the panic; a longer
	// input with a body stays in bounds and passes under both the fixed
	// and the reverted implementation.
	in := "\x89wArn:"
	if got := dedupWarnings(in); got != in {
		t.Errorf("dedupWarnings = %q, want unchanged %q", got, in)
	}
}

func TestDedupWarningsIgnoresNonWarningLines(t *testing.T) {
	in := "same\nsame\nsame"
	if got := dedupWarnings(in); got != in {
		t.Errorf("dedupWarnings = %q, want unchanged %q", got, in)
	}
}

func TestDedupWarningsIsDeterministic(t *testing.T) {
	in := "warning: z\nwarning: a\nwarning: z\nwarning: a\nwarning: m\nwarning: m"
	first := dedupWarnings(in)
	for i := 0; i < 20; i++ {
		if got := dedupWarnings(in); got != first {
			t.Fatalf("run %d gave %q, first gave %q", i, got, first)
		}
	}
}

func TestDropProgressRemovesOverwrittenLines(t *testing.T) {
	in := "start\ndownloading 10%\rdownloading 90%\ndone"
	if got, want := dropProgress(in), "start\ndone"; got != want {
		t.Errorf("dropProgress = %q, want %q", got, want)
	}
}

func TestDropProgressKeepsCRLFLines(t *testing.T) {
	// A trailing \r is a line terminator, not overwritten output.
	in := "start\r\ndone\r"
	if got, want := dropProgress(in), "start\r\ndone\r"; got != want {
		t.Errorf("dropProgress = %q, want %q", got, want)
	}
}

func TestDropProgressLeavesPlainTextAlone(t *testing.T) {
	in := "a\nb\nc"
	if got := dropProgress(in); got != in {
		t.Errorf("dropProgress = %q, want unchanged %q", got, in)
	}
}

func TestStagesHandleEmptyInput(t *testing.T) {
	for name, fn := range map[string]func(string) string{
		"stripANSI":     stripANSI,
		"collapseRuns":  collapseRuns,
		"dedupWarnings": dedupWarnings,
		"dropProgress":  dropProgress,
	} {
		if got := fn(""); got != "" {
			t.Errorf("%s(\"\") = %q, want empty", name, got)
		}
	}
}
