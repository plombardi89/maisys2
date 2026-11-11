package system

import (
	"syscall"
	"testing"
)

// processGroup says which process group the process runs in. Windows
// doesn't tell a process, so it's always "unknown".
func processGroup() string {
	return "unknown"
}

// processSession says which session the process runs in. Windows has
// none, so it's always "unknown".
func processSession() string {
	return "unknown"
}

// TestProcAttr checks that commands start in a new process group, which
// Windows starts with Ctrl-C turned off, unless they're interactive.
func TestProcAttr(t *testing.T) {
	for _, sudo := range []bool{false, true} {
		if attr := procAttr(false, sudo); attr == nil || attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
			t.Errorf("procAttr(false, %t) = %+v, want CREATE_NEW_PROCESS_GROUP", sudo, attr)
		}
	}
	if attr := procAttr(true, false); attr != nil {
		t.Errorf("procAttr(true, false) = %+v, want nil", attr)
	}
}
