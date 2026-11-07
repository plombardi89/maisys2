//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh puts the locked byte at 4 GiB, well past the process ID
// at the start of the file, so other processes can still read the ID.
const lockOffsetHigh = 1

// tryLock takes an exclusive lock on f without waiting, and reports whether
// it got it. The lock belongs to f's handle, so a second handle to the same
// file can't take it either, even in the same process.
func tryLock(f *os.File) (bool, error) {
	ol := windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	ol := windows.Overlapped{OffsetHigh: lockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
