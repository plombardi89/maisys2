//go:build windows

package fsutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// retryDelays are how long rename waits before each new try, about 1.6
// seconds in all.
var retryDelays = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
	160 * time.Millisecond, 320 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond,
}

func rename(oldpath, newpath string) error {
	err := os.Rename(oldpath, newpath)
	for _, delay := range retryDelays {
		if !inUse(err) {
			break
		}
		time.Sleep(delay)
		err = os.Rename(oldpath, newpath)
	}
	return err
}

// inUse reports whether err is how Windows refuses to replace a file that
// another process has open without sharing delete access.
func inUse(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
