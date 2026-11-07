//go:build !windows

package system

import (
	"os"
	"syscall"
)

// sudoSupported reports whether maisys uses sudo on this OS.
const sudoSupported = true

// interrupt asks a command to stop. sudo passes SIGTERM on to the command
// it runs as root, which it couldn't do with SIGKILL.
func interrupt(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// procAttr returns how to start a command. One that isn't interactive gets
// a process group of its own, so the SIGINT a terminal sends maisys's
// process group on Ctrl-C doesn't reach it. It stays in maisys's session,
// with the same terminal, which sudo's cached password depends on.
func procAttr(interactive bool) *syscall.SysProcAttr {
	if interactive {
		return nil
	}
	return &syscall.SysProcAttr{Setpgid: true}
}
