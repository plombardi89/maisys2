package system

import (
	"os"
	"syscall"
)

// sudoSupported reports whether maisys uses sudo on this OS. On Windows,
// steps that need admin rights run in an elevated helper instead.
const sudoSupported = false

// interrupt stops a command. Windows has no signal to ask it to.
func interrupt(p *os.Process) error {
	return p.Kill()
}

// procAttr returns how to start a command. One that isn't interactive
// starts a new process group, which Windows starts with Ctrl-C turned off,
// so a Ctrl-C in the console doesn't reach it. Windows has no sudo, so
// whether the command runs it makes no difference.
func procAttr(interactive, _ bool) *syscall.SysProcAttr {
	if interactive {
		return nil
	}
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
