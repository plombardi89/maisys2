//go:build !windows

package system

import (
	"os"
	"syscall"
)

// sudoSupported reports whether maisys uses sudo on this OS.
const sudoSupported = true

// interrupt asks a command to stop, with SIGTERM. One that isn't
// interactive leads a process group, which holds what it started too, such
// as an installer script's downloads, so the whole group is asked. The user
// can't signal a command run as root, but sudo passes SIGTERM on to it,
// which it couldn't do with SIGKILL.
func interrupt(p *os.Process) error {
	if pgid, err := syscall.Getpgid(p.Pid); err == nil && pgid == p.Pid {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	return p.Signal(syscall.SIGTERM)
}

// procAttr returns how to start a command. One that isn't interactive
// starts a session of its own: the SIGINT a terminal sends maisys's process
// group on Ctrl-C doesn't reach it, and it has no terminal, so if it opens
// /dev/tty to ask something, as ssh does about a host key it doesn't know,
// that fails at once, rather than stopping it for good as a background
// job. A command that runs sudo only gets a process group of its own, in
// maisys's session, with the same terminal, which sudo's record of a typed
// password depends on.
func procAttr(interactive, sudo bool) *syscall.SysProcAttr {
	switch {
	case interactive:
		return nil
	case sudo:
		return &syscall.SysProcAttr{Setpgid: true}
	default:
		return &syscall.SysProcAttr{Setsid: true}
	}
}
