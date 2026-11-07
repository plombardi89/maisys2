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
