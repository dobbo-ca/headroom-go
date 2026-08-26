package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/paths"
)

// TestMain keeps this package's tests out of the developer's real ~/.headroom.
//
// `headroom proxy` and `headroom wrap` open a CCR store AND a ledger from
// HEADROOM_HOME. A test that exercises either and forgets to set it writes
// into real user data — and the ledger is precisely what `headroom perf`
// reports on, so the pollution surfaces later as fabricated turns in a real
// report. TestProxyModelFlagReachesTheTokenizer did exactly that: two entries
// per run, claiming 25,659 bytes compressed that no agent ever sent.
//
// Defaulting it for the whole package fixes every present and future test at
// once. A test that wants its own directory still calls t.Setenv, which wins.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "headroom-cmd-test")
	if err != nil {
		panic("isolate HEADROOM_HOME: " + err.Error())
	}
	if err := os.Setenv("HEADROOM_HOME", dir); err != nil {
		panic("isolate HEADROOM_HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// The guard above is only worth having if it actually diverts paths.Home, and
// only meaningful if it diverts it away from the real one.
func TestHeadroomHomeIsIsolatedFromTheRealOne(t *testing.T) {
	home, err := paths.Home()
	if err != nil {
		t.Fatal(err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(userHome, ".headroom")
	if home == real {
		t.Fatalf("paths.Home() = %q, the developer's real headroom directory; "+
			"a test that starts a proxy would write turns into their ledger", home)
	}
	if os.Getenv("HEADROOM_HOME") == "" {
		t.Error("HEADROOM_HOME is unset, so paths.Home() falls back to the real directory")
	}
}
