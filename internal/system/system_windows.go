package system

import "os"

// sudoSupported reports whether maisys uses sudo on this OS. On Windows,
// steps that need admin rights run in an elevated helper instead.
const sudoSupported = false

// interrupt stops a command. Windows has no signal to ask it to.
func interrupt(p *os.Process) error {
	return p.Kill()
}
