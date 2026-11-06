//go:build windows

package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
)

// swap puts newPath in place of exe. Windows won't overwrite a running
// .exe, but it will rename one, so exe is first moved aside to exe.old.
// RemoveOld deletes it on a later run.
func swap(newPath, exe string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// An earlier exe.old is still running; use a unique name instead.
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		old = exe + ".old-" + hex.EncodeToString(b)
	}
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("moving the running binary aside: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		if restoreErr := os.Rename(old, exe); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the old binary failed: %w)", err, restoreErr)
		}
		return err
	}
	return nil
}

// Restart runs exe with args and env, connected to this process's console,
// and returns its exit code. Windows has no exec, so the new binary runs as a
// child process.
func Restart(ctx context.Context, exe string, args, env []string) (int, error) {
	// The child handles Ctrl-C itself, so don't kill it when ctx is cancelled.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}
