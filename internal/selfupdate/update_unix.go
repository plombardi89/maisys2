//go:build !windows

package selfupdate

import (
	"context"
	"os"
	"syscall"
)

// swap renames newPath over exe. Unix lets a running binary be replaced:
// the running process keeps the old file until it exits.
func swap(newPath, exe string) error {
	return os.Rename(newPath, exe)
}

// Restart replaces the current process with exe, run with args and env. It
// only returns if that fails.
func Restart(_ context.Context, exe string, args, env []string) (int, error) {
	return 0, syscall.Exec(exe, append([]string{exe}, args...), env)
}
