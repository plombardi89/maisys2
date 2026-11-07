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

// TestProcAttr checks that commands start in a new process group, which
// Windows starts with Ctrl-C turned off, unless they're interactive.
func TestProcAttr(t *testing.T) {
	if attr := procAttr(false); attr == nil || attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("procAttr(false) = %+v, want CREATE_NEW_PROCESS_GROUP", attr)
	}
	if attr := procAttr(true); attr != nil {
		t.Errorf("procAttr(true) = %+v, want nil", attr)
	}
}
