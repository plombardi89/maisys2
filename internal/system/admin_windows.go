//go:build windows

package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// helperConnectWait is how long the elevated helper has to connect to its
// pipe once it's started.
const helperConnectWait = 30 * time.Second

// newAdminSession returns the session for the commands that need admin
// rights.
func newAdminSession(r *ExecRunner) *adminSession {
	return &adminSession{r: r, elevated: windows.GetCurrentProcessToken().IsElevated(), launch: launchHelper}
}

// launchHelper starts the elevated helper: maisys's own executable, with
// the hidden HelperCommand, the name of a new pipe and secret. Windows
// shows the UAC prompt for it. It waits for the helper to connect to the
// pipe, and checks that the helper is the process that did.
//
// The pipe has a random name, takes one connection from this computer
// only, and only you, admins and the system may connect to it: an admin
// rather than you if you aren't one and typed an admin's credentials at
// the UAC prompt. The helper opens it so that maisys can't act as the
// helper's user (SECURITY_IDENTIFICATION).
func launchHelper(ctx context.Context, secret string) (*helperProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	name, err := newHelperPipeName()
	if err != nil {
		return nil, fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	pipe, err := createHelperPipe(name)
	if err != nil {
		return nil, fmt.Errorf("couldn't start the elevated helper: creating its pipe: %w", err)
	}
	proc, err := shellExecuteRunas(exe, []string{HelperCommand, "--pipe", name, "--secret", secret})
	if err != nil {
		_ = windows.CloseHandle(pipe)
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return nil, errUACDeclined
		}
		return nil, fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	wait := func(timeout time.Duration) error {
		defer func() { _ = windows.CloseHandle(proc) }()
		event, err := windows.WaitForSingleObject(proc, uint32(timeout.Milliseconds()))
		if err != nil {
			return err
		}
		if event != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("the elevated helper didn't exit within %s", timeout)
		}
		return nil
	}

	err = connectPipe(ctx, pipe, proc)
	if err == nil {
		err = checkClient(pipe, proc)
	}
	if err != nil {
		_ = windows.CloseHandle(pipe)
		_ = wait(helperExitWait)
		return nil, fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	// The pipe was made for overlapped I/O, so the file reads and writes it
	// through Go's poller, and Close stops them.
	return &helperProcess{conn: os.NewFile(uintptr(pipe), name), wait: wait}, nil
}

// createHelperPipe creates the server end of the elevated helper's pipe.
func createHelperPipe(name string) (windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	// Full access for the system, the Administrators group, and you, and
	// nobody else.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return 0, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateNamedPipe(n,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 64<<10, 64<<10, 0, sa)
}

// connectPipe waits for the helper, the process proc, to connect to pipe,
// for up to helperConnectWait. It stops waiting if the helper exits, or ctx
// is done.
func connectPipe(ctx context.Context, pipe, proc windows.Handle) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(event) }()
	ov := &windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(pipe, ov)
	switch {
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		return nil
	case !errors.Is(err, windows.ERROR_IO_PENDING):
		return err
	}

	cancel := func() {
		_ = windows.CancelIoEx(pipe, ov)
		// Wait for the cancellation, so ov is no longer in use.
		var n uint32
		_ = windows.GetOverlappedResult(pipe, ov, &n, true)
	}
	deadline := time.Now().Add(helperConnectWait)
	for {
		which, err := windows.WaitForMultipleObjects([]windows.Handle{event, proc}, false, 200)
		switch {
		case err != nil:
			cancel()
			return err
		case which == windows.WAIT_OBJECT_0:
			var n uint32
			return windows.GetOverlappedResult(pipe, ov, &n, false)
		case which == windows.WAIT_OBJECT_0+1:
			cancel()
			var code uint32
			_ = windows.GetExitCodeProcess(proc, &code)
			return fmt.Errorf("it exited with code %d before it connected", code)
		case ctx.Err() != nil:
			cancel()
			return ctx.Err()
		case time.Now().After(deadline):
			cancel()
			return fmt.Errorf("it didn't connect within %s", helperConnectWait)
		}
	}
}

// checkClient checks that the process connected to pipe is proc.
func checkClient(pipe, proc windows.Handle) error {
	var client uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &client); err != nil {
		return err
	}
	helper, err := windows.GetProcessId(proc)
	if err != nil {
		return err
	}
	if client != helper {
		return fmt.Errorf("another process (%d) connected to its pipe", client)
	}
	return nil
}

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW  = shell32.NewProc("ShellExecuteExW")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

// ShellExecuteEx's flags.
const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
)

// shellExecuteRunas starts exe with args elevated, through ShellExecuteEx's
// "runas" verb, which shows the UAC prompt, with its console window hidden.
// It returns the process, which the caller must close.
func shellExecuteRunas(exe string, args []string) (windows.Handle, error) {
	// ShellExecuteEx may use COM, which needs the thread initialised.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil ||
		errors.Is(err, syscall.Errno(windows.S_FALSE)) {
		defer windows.CoUninitialize()
	}

	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	// The UAC prompt belongs to the console's window, if there is one, so
	// it comes to the front.
	console, _, _ := procGetConsoleWindow.Call()
	info := &shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		hwnd:         windows.Handle(console),
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(*info))
	ok, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(info)))
	if ok == 0 {
		return 0, err
	}
	if info.hProcess == 0 {
		return 0, errors.New("windows didn't return the process it started")
	}
	return info.hProcess, nil
}

// ServeElevated runs the elevated helper, which the hidden HelperCommand
// starts: it connects to pipe, which maisys made for it, says hello with
// secret, and runs the commands maisys sends there, until maisys is done.
// It refuses to serve unless it's elevated.
func ServeElevated(ctx context.Context, pipe, secret string) error {
	if err := checkHelperPipe(pipe); err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(pipe)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return fmt.Errorf("couldn't connect to maisys: %w", err)
	}
	conn := os.NewFile(uintptr(h), pipe)
	defer func() { _ = conn.Close() }()
	return serveHelper(ctx, conn, secret, windows.GetCurrentProcessToken().IsElevated())
}
