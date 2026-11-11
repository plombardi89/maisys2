package unit

import "testing"

// SetBootedDir makes the package find whether systemd runs the system with
// the folder dir, rather than /run/systemd/system, until tb ends.
func SetBootedDir(tb testing.TB, dir string) {
	tb.Helper()
	old := bootedDir
	bootedDir = dir
	tb.Cleanup(func() { bootedDir = old })
}
