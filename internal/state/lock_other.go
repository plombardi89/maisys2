//go:build (!unix && !windows) || aix

package state

import (
	"fmt"
	"os"
	"runtime"
)

// tryLock fails: maisys only locks files on Linux, Windows and other
// systems with flock.
func tryLock(*os.File) (bool, error) {
	return false, fmt.Errorf("locking files isn't supported on %s", runtime.GOOS)
}

func unlock(*os.File) error {
	return nil
}
