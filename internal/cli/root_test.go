package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	getenv := func(string) string { return "" }
	code = Execute(context.Background(), args, &out, &errOut, getenv)
	return code, out.String(), errOut.String()
}

func TestVersionFlag(t *testing.T) {
	code, stdout, stderr := run(t, "--version")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "maisys dev") {
		t.Errorf("stdout = %q, want it to start with %q", stdout, "maisys dev")
	}
}

func TestHelpListsCommands(t *testing.T) {
	code, stdout, _ := run(t, "--help")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	for _, name := range []string{"sync", "config", "status", "--verbose", "--version"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help output doesn't mention %q:\n%s", name, stdout)
		}
	}
}

func TestPlaceholderCommands(t *testing.T) {
	tests := []struct {
		args []string
		name string
	}{
		{[]string{"sync"}, "sync"},
		{[]string{"sync", "https://example.com/config.git", "--dry-run", "--only", "golang,kubectl"}, "sync"},
		{[]string{"sync", "-n", "-y", "--local", "--machine", "box", "--dir", "/tmp/cfg"}, "sync"},
		{[]string{"config", "--explain"}, "config"},
		{[]string{"config", "init", "/tmp/cfg", "--machine", "box", "--remote", "https://example.com/c.git"}, "config init"},
		{[]string{"config", "validate", "--all"}, "config validate"},
		{[]string{"status", "-v"}, "status"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, stderr := run(t, tt.args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			want := `maisys: "` + tt.name + `" is not implemented yet`
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"sync", "a", "b"}, "accepts at most 1 arg"},
		{[]string{"sync", "--bogus"}, "Run 'maisys sync --help' for usage"},
		{[]string{"config", "extra"}, `unknown command "extra"`},
		{[]string{"nope"}, `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, stderr := run(t, tt.args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.want)
			}
		})
	}
}

func TestSyncSelfUpdateStep(t *testing.T) {
	// Tests run as a development build, which never checks for updates, so
	// these don't touch the network.
	const devMsg = "not checking for a newer maisys: this is a development build"

	_, _, stderr := run(t, "sync", "-v")
	if !strings.Contains(stderr, devMsg) {
		t.Errorf("sync -v: stderr = %q, want it to contain %q", stderr, devMsg)
	}

	_, _, stderr = run(t, "sync", "-v", "--local")
	if strings.Contains(stderr, devMsg) {
		t.Errorf("sync -v --local: stderr = %q, want no self-update step", stderr)
	}

	_, _, stderr = run(t, "sync")
	if strings.Contains(stderr, devMsg) {
		t.Errorf("sync without -v: stderr = %q, want the message only with --verbose", stderr)
	}
}

func TestNoColor(t *testing.T) {
	tests := []struct {
		value string
		color bool
	}{
		{"", true},
		{"1", false},
		{"0", false}, // any non-empty value turns colour off
	}
	for _, tt := range tests {
		g := newGlobalOptions(nil, func(string) string { return tt.value })
		if g.color != tt.color {
			t.Errorf("NO_COLOR=%q: color = %v, want %v", tt.value, g.color, tt.color)
		}
	}
}
