//go:build !windows

package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
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

// processSession says which session the process runs in, the same way as
// processGroup.
func processSession() string {
	sid, err := unix.Getsid(0)
	if err != nil {
		return "unknown"
	}
	if sid == os.Getpid() {
		return "own"
	}
	if parent, err := unix.Getsid(os.Getppid()); err == nil && parent == sid {
		return "parent"
	}
	return "other"
}

// TestProcAttr checks how commands start: an interactive one in maisys's
// process group, one that runs sudo in a process group of its own in
// maisys's session, and every other one in a session of its own.
func TestProcAttr(t *testing.T) {
	tests := []struct {
		interactive, sudo bool
		want              *syscall.SysProcAttr
	}{
		{want: &syscall.SysProcAttr{Setsid: true}},
		{sudo: true, want: &syscall.SysProcAttr{Setpgid: true}},
		{interactive: true},
		{interactive: true, sudo: true},
	}
	for _, tt := range tests {
		if got := procAttr(tt.interactive, tt.sudo); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("procAttr(%t, %t) = %+v, want %+v", tt.interactive, tt.sudo, got, tt.want)
		}
	}
}

// TestRunProcessGroup checks that commands run in a session of their own,
// where the SIGINT a terminal sends maisys's process group on Ctrl-C
// doesn't reach them, unless they're interactive.
func TestRunProcessGroup(t *testing.T) {
	r := NewRunner(Options{})
	for _, tt := range []struct {
		interactive bool
		want        string
	}{
		{interactive: false, want: "own/own"},
		{interactive: true, want: "parent/parent"},
	} {
		c := helperCmd(t, "group")
		c.Interactive = tt.interactive
		res, err := r.Run(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(res.Stdout)); got != tt.want {
			t.Errorf("Interactive = %t: the command ran in process group and session %q, want %q", tt.interactive, got, tt.want)
		}
	}
}

// TestRunWithoutTerminal runs, from a terminal, a command that asks a
// question there, as ssh does about a host key it doesn't know: it must
// fail at once, rather than be stopped for reading the terminal from the
// background, which would leave maisys waiting for good.
func TestRunWithoutTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, testBinary(t), "run", "tty")
	cmd.Env = append(os.Environ(), fakeEnv+"=helper")
	// The runner runs in a session whose terminal is the pseudo-terminal,
	// as maisys does in a terminal.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	// Reading fails once the runner is done with the terminal.
	out, _ := io.ReadAll(ptmx)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the runner failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "exit code 3:") || !strings.Contains(string(out), "/dev/tty") {
		t.Errorf("the command that asks on the terminal: %q, want it to fail to open /dev/tty (exit code 3)", out)
	}
}

// TestRunCancelStopsWhatItStarted cancels a command that started another
// in the background, as an installer script starts its downloads: both
// must stop.
func TestRunCancelStopsWhatItStarted(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("telling whether a process runs needs /proc")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewRunner(Options{}).Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `sleep 60 & echo $! > "$1"; wait`, "sh", pidFile}})
		done <- err
	}()
	var pid int
	eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Run() didn't return within 5s of being cancelled")
	}
	eventually(t, func() bool { return !running(pid) })
}

// running reports whether process pid runs: it exists, and isn't a zombie
// waiting for its parent.
func running(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// The state follows the command's name, which is in parentheses.
	i := bytes.LastIndexByte(data, ')')
	return i < 0 || i+2 >= len(data) || data[i+2] != 'Z'
}
