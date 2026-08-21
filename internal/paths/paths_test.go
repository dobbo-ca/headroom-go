package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHomeUsesHeadroomHomeOverride(t *testing.T) {
	t.Setenv("HEADROOM_HOME", "/tmp/custom-headroom")
	got, err := Home()
	if err != nil {
		t.Fatalf("Home() error: %v", err)
	}
	if got != "/tmp/custom-headroom" {
		t.Errorf("Home() = %q, want /tmp/custom-headroom", got)
	}
}

func TestHomeDefaultsToDotHeadroomUnderUserHome(t *testing.T) {
	t.Setenv("HEADROOM_HOME", "")
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no user home dir on this platform: %v", err)
	}
	got, err := Home()
	if err != nil {
		t.Fatalf("Home() error: %v", err)
	}
	if want := filepath.Join(userHome, ".headroom"); got != want {
		t.Errorf("Home() = %q, want %q", got, want)
	}
}

func TestLayoutPathsHangOffHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HEADROOM_HOME", base)

	db, err := CCRDBPath()
	if err != nil {
		t.Fatalf("CCRDBPath() error: %v", err)
	}
	if want := filepath.Join(base, "ccr.db"); db != want {
		t.Errorf("CCRDBPath() = %q, want %q", db, want)
	}
}

func TestEnsureDirCreatesNestedDirAndIsIdempotent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir() error: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat after EnsureDir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("EnsureDir(%q) did not create a directory", target)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("EnsureDir(%q) mode = %#o, want 0700: CCR payloads must stay owner-only", target, perm)
		}
	}
	if err := EnsureDir(target); err != nil {
		t.Errorf("second EnsureDir() error: %v", err)
	}
}
