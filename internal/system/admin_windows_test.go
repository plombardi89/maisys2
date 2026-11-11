//go:build windows

package system

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestShellExecuteInfoSize(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("maisys only runs on Windows on amd64")
	}
	// sizeof(SHELLEXECUTEINFOW) on 64-bit Windows.
	if size := unsafe.Sizeof(shellExecuteInfo{}); size != 112 {
		t.Errorf("shellExecuteInfo is %d bytes, want 112", size)
	}
}

// TestHelperOverPipe serves the elevated helper in the test process over a
// real helper pipe, as maisys and the helper it starts elevated do, but
// without starting another process: the test process is both ends, so
// that's the client the pipe checks for. Unless the tests run elevated, as
// on CI, the helper only says it can't serve.
func TestHelperOverPipe(t *testing.T) {
	name, err := newHelperPipeName()
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := createHelperPipe(name)
	if err != nil {
		t.Fatalf("createHelperPipe() error = %v", err)
	}
	if _, err := createHelperPipe(name); err == nil {
		t.Error("a second pipe with the same name was created")
	}

	const secret = "0123456789abcdef"
	served := make(chan error, 1)
	go func() { served <- ServeElevated(t.Context(), name, secret) }()

	// A real handle to this process, not the pseudo-handle CurrentProcess
	// returns, which WaitForMultipleObjects doesn't accept. Production code
	// passes the helper's process handle.
	self, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(self) }()
	if err := connectPipe(t.Context(), pipe, self); err != nil {
		_ = windows.CloseHandle(pipe)
		t.Fatalf("connectPipe() error = %v", err)
	}
	if err := checkClient(pipe, self); err != nil {
		t.Errorf("checkClient() error = %v", err)
	}
	conn := os.NewFile(uintptr(pipe), name)
	client, err := connectHelper(conn, secret, helperHelloWait)
	if !windows.GetCurrentProcessToken().IsElevated() {
		if err == nil || !strings.Contains(err.Error(), "isn't running as admin") {
			t.Errorf("connectHelper() unelevated: error = %v, want one saying the helper isn't elevated", err)
		}
		if err := <-served; err == nil {
			t.Error("ServeElevated() unelevated: no error")
		}
		return
	}
	if err != nil {
		t.Fatalf("connectHelper() error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	c := helperCmd(t, "exit", "3", "out\n", "err\n")
	res, err := client.run(t.Context(), helperCommand{Name: c.Name, Args: c.Args, Env: c.Env}, &stdout, &stderr)
	if err != nil || res.ExitCode != 3 || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Errorf("run() = %+v, %v, output %q, error output %q; want exit code 3, out and err", res, err, stdout.String(), stderr.String())
	}
	client.close()
	if err := <-served; err != nil {
		t.Errorf("ServeElevated() error = %v, want nil once maisys is done", err)
	}
}

// TestServeElevatedWithoutMaisys checks that the helper fails when there's
// no pipe to connect to.
func TestServeElevatedWithoutMaisys(t *testing.T) {
	name, err := newHelperPipeName()
	if err != nil {
		t.Fatal(err)
	}
	if err := ServeElevated(t.Context(), name, "secret"); err == nil || !strings.Contains(err.Error(), "couldn't connect to maisys") {
		t.Errorf("ServeElevated() error = %v, want one saying it couldn't connect", err)
	}
}
