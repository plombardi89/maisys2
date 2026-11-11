package system_test

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func TestInstallFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("maisys only writes files as root on Linux")
	}
	f := systemtest.NewFakeRunner(t)
	var tmp string
	f.On("install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if len(c.Args) != 8 {
			t.Fatalf("install ran with %q", c.Args)
		}
		tmp = c.Args[6]
		want := []string{"-m", "0440", "-o", "root", "-g", "root", tmp, "/etc/sudoers.d/tester"}
		if !slices.Equal(c.Args, want) {
			t.Errorf("install ran with %q, want %q", c.Args, want)
		}
		data, err := os.ReadFile(tmp)
		if err != nil || string(data) != "tester ALL=(ALL) NOPASSWD: ALL\n" {
			t.Errorf("the temporary file has %q, %v; want the file's contents", data, err)
		}
		return system.Result{}, nil
	})

	err := system.InstallFile(t.Context(), f, []byte("tester ALL=(ALL) NOPASSWD: ALL\n"), "/etc/sudoers.d/tester", 0o440)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.RootCommands(); len(got) != 1 {
		t.Errorf("commands run as root = %q, want install", got)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary file %s is still there: %v", tmp, err)
	}

	f.On("install", systemtest.Fail(1, "install: cannot create regular file '/etc/x': No such file or directory\n"))
	err = system.InstallFile(t.Context(), f, nil, "/etc/x", 0o644)
	if exitErr, ok := errors.AsType[*system.ExitError](err); !ok || exitErr.Label != "writing /etc/x" {
		t.Errorf("InstallFile() error = %v, want an *ExitError for writing /etc/x", err)
	}

	if err := system.InstallFile(t.Context(), f, nil, "etc/x", 0o644); err == nil {
		t.Error("InstallFile() to a relative path: no error")
	}

	f = systemtest.NewFakeRunner(t)
	f.SudoStatus = system.SudoNeedsPassword
	if err := system.InstallFile(t.Context(), f, nil, "/etc/x", 0o644); !errors.Is(err, system.ErrNeedsPassword) {
		t.Errorf("InstallFile() without a sudo session: error = %v, want ErrNeedsPassword", err)
	}
}
