//go:build !windows

package system

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// processGroup says which process group the process runs in: "own" if it
// leads a group of its own, "parent" if it's in its parent's, and "other"
// otherwise.
func processGroup() string {
	pgrp := syscall.Getpgrp()
	if pgrp == os.Getpid() {
		return "own"
	}
	if parent, err := syscall.Getpgid(os.Getppid()); err == nil && parent == pgrp {
		return "parent"
	}
	return "other"
}

// TestRunProcessGroup checks that commands run in a process group of their
// own, where the SIGINT a terminal sends maisys's group on Ctrl-C doesn't
// reach them, unless they're interactive.
func TestRunProcessGroup(t *testing.T) {
	r := NewRunner(Options{})
	for _, tt := range []struct {
		interactive bool
		want        string
	}{
		{interactive: false, want: "own"},
		{interactive: true, want: "parent"},
	} {
		c := helperCmd(t, "group")
		c.Interactive = tt.interactive
		res, err := r.Run(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(res.Stdout)); got != tt.want {
			t.Errorf("Interactive = %t: the command ran in process group %q, want %q", tt.interactive, got, tt.want)
		}
	}
}
