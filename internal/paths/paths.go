// Package paths resolves the ~/.headroom on-disk layout. HEADROOM_HOME
// overrides the base directory, which is what tests and container images use.
package paths

import (
	"os"
	"path/filepath"
)

// Home returns the headroom base directory: HEADROOM_HOME if set and non-empty,
// otherwise ~/.headroom. The environment is read on every call so a process can
// be reconfigured without restarting.
func Home() (string, error) {
	if h := os.Getenv("HEADROOM_HOME"); h != "" {
		return h, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".headroom"), nil
}

// CCRDBPath returns the SQLite CCR store file path.
func CCRDBPath() (string, error) {
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "ccr.db"), nil
}

// EnsureDir creates dir and any missing parents. The 0o700 mode keeps CCR
// payloads, which are verbatim tool output, readable only by their owner.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
