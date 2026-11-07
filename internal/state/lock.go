package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrLocked is wrapped by Acquire's error when another process holds the
// lock.
var ErrLocked = errors.New("another maisys sync is running")

// Lock is the held lock that stops two syncs running at once.
type Lock struct {
	f *os.File
}

// Acquire takes the lock in the lock file at path, creating the file and
// its folder (mode 0700) if needed, and writes this process's ID into it.
// It doesn't wait: if another process holds the lock, the error wraps
// ErrLocked and gives that process's ID. The OS releases the lock when the
// process holding it exits, however it exits, so a lock file left behind
// never stops a sync.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("taking the lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("taking the lock: %w", err)
	}
	held, err := tryLock(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("taking the lock %s: %w", path, err)
	}
	if !held {
		pid := readPID(f)
		_ = f.Close()
		if pid == 0 {
			return nil, fmt.Errorf("%w; wait for it to finish, then try again", ErrLocked)
		}
		return nil, fmt.Errorf("%w (process %d); wait for it to finish, then try again", ErrLocked, pid)
	}

	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	err = f.Truncate(0)
	if err == nil {
		_, err = f.WriteAt(pid, 0)
	}
	if err != nil {
		_ = unlock(f)
		_ = f.Close()
		return nil, fmt.Errorf("taking the lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// readPID returns the process ID in the lock file f, or 0 if there isn't
// one. The process holding the lock may not have written it yet.
func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// Release empties the lock file and releases the lock. The file stays, so
// another process that has it open can't end up locking a deleted file.
// Calling Release again does nothing.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	err := errors.Join(f.Truncate(0), unlock(f), f.Close())
	if err != nil {
		return fmt.Errorf("releasing the lock: %w", err)
	}
	return nil
}
