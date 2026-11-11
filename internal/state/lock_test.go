package state

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func lockedMessage(pid int) string {
	return fmt.Sprintf("another maisys sync is running (process %d); wait for it to finish, then try again", pid)
}

func TestAcquire(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "maisys")
	path := filepath.Join(dir, "lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got, want := readTestFile(t, path), fmt.Sprintf("%d\n", os.Getpid()); got != want {
		t.Errorf("lock file holds %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		checkMode(t, path, 0o600)
		checkMode(t, dir, 0o700|fs.ModeDir)
	}

	// A second open of the lock file can't take the lock, even in the same
	// process.
	_, err = Acquire(path)
	if !errors.Is(err, ErrLocked) || err.Error() != lockedMessage(os.Getpid()) {
		t.Errorf("Acquire while locked: error = %v, want %q", err, lockedMessage(os.Getpid()))
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := readTestFile(t, path); got != "" {
		t.Errorf("after Release, lock file holds %q, want nothing", got)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("Release again: %v", err)
	}

	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireBeforeTheHolderWritesItsID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if held, err := tryLock(f); !held || err != nil {
		t.Fatalf("tryLock = %v, %v", held, err)
	}
	defer func() { _ = unlock(f) }()

	_, err = Acquire(path)
	want := "another maisys sync is running; wait for it to finish, then try again"
	if !errors.Is(err, ErrLocked) || err.Error() != want {
		t.Errorf("Acquire error = %v, want %q", err, want)
	}
}

// TestAcquireHeldByAnotherProcess runs a helper process that takes the lock,
// then checks that Acquire names it, and that the lock is free again once
// the helper dies without releasing it.
func TestAcquireHeldByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "STATE_HELPER_LOCK="+path)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper printed %q (%v), want locked", line, err)
	}

	_, err = Acquire(path)
	if want := lockedMessage(cmd.Process.Pid); !errors.Is(err, ErrLocked) || err.Error() != want {
		t.Errorf("Acquire while the helper holds the lock: error = %v, want %q", err, want)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// The OS may take a moment to release a dead process's lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		lock, err := Acquire(path)
		if err == nil {
			if err := lock.Release(); err != nil {
				t.Errorf("Release: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Acquire after the helper died: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHelperProcess isn't a real test. TestAcquireHeldByAnotherProcess runs
// the test binary with STATE_HELPER_LOCK set to make it hold the lock in
// that file until it's killed or its stdin is closed.
func TestHelperProcess(_ *testing.T) {
	path := os.Getenv("STATE_HELPER_LOCK")
	if path == "" {
		return
	}
	if _, err := Acquire(path); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
