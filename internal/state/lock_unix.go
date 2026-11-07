//go:build unix && !aix

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock on f without waiting, and reports
// whether it got it. flock locks belong to the open file, so a second open
// of the same file can't take the lock either, even in the same process.
func tryLock(f *os.File) (bool, error) {
	err := flock(f, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	return flock(f, unix.LOCK_UN)
}

func flock(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
