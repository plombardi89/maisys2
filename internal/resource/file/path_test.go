package file_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource/file"
)

func TestExpand(t *testing.T) {
	home := filepath.Join(t.TempDir(), "phil")
	abs := filepath.Join(t.TempDir(), "etc", "wsl.conf")
	tests := []struct {
		in, want string
		err      bool
	}{
		{in: "~/.gitconfig", want: filepath.Join(home, ".gitconfig")},
		{in: "~/.config/nvim/init.lua", want: filepath.Join(home, ".config", "nvim", "init.lua")},
		{in: "~/.config/nvim/", want: filepath.Join(home, ".config", "nvim")},
		{in: "~", want: home},
		{in: abs, want: abs},
		{in: "~/../x", err: true},
		{in: "~/a/../../x", err: true},
		{in: ".gitconfig", err: true},
		{in: "~phil/.gitconfig", err: true},
	}
	for _, tt := range tests {
		got, err := file.Expand(home, tt.in)
		if got != tt.want || (err != nil) != tt.err {
			t.Errorf("Expand(%q) = %q, %v; want %q, error %t", tt.in, got, err, tt.want, tt.err)
		}
	}
	if _, err := file.Expand("", "~/.gitconfig"); err == nil {
		t.Error("Expand() without a home folder: no error")
	}
}

func TestDisplay(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "wsl.conf")
	tests := []struct{ in, want string }{
		{"~/.gitconfig", "~/.gitconfig"},
		{"~/.config//nvim/./init.lua", "~/.config/nvim/init.lua"},
		{"~/.config/nvim/", "~/.config/nvim"},
		{"~", "~"},
		{"~/", "~"},
		{abs, abs},
	}
	for _, tt := range tests {
		if got := file.Display(tt.in); got != tt.want {
			t.Errorf("Display(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShort(t *testing.T) {
	home := filepath.Join(t.TempDir(), "phil")
	other := filepath.Join(t.TempDir(), "opt", "go")
	tests := []struct{ in, want string }{
		{filepath.Join(home, ".local", "bin", "go"), "~/.local/bin/go"},
		{home, "~"},
		{other, other},
		{home + "x", home + "x"},
		{"relative/go", "relative/go"},
	}
	for _, tt := range tests {
		if got := file.Short(home, tt.in); got != tt.want {
			t.Errorf("Short(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows paths have drive letters and backslashes")
	}
	const home = `C:\Users\phil`
	tests := []struct{ in, want, display string }{
		{"~/.wslconfig", `C:\Users\phil\.wslconfig`, "~/.wslconfig"},
		{`~\.wslconfig`, `C:\Users\phil\.wslconfig`, "~/.wslconfig"},
		{`~/AppData/Local/x\y.json`, `C:\Users\phil\AppData\Local\x\y.json`, "~/AppData/Local/x/y.json"},
		{`D:\config\x.toml`, `D:\config\x.toml`, `D:\config\x.toml`},
	}
	for _, tt := range tests {
		got, err := file.Expand(home, tt.in)
		if err != nil || got != tt.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if got := file.Display(tt.in); got != tt.display {
			t.Errorf("Display(%q) = %q, want %q", tt.in, got, tt.display)
		}
	}
	if _, err := file.Expand(home, `~\..\x`); err == nil {
		t.Error(`Expand("~\..\x"): no error`)
	}
	if got := file.Short(home, `C:\Users\phil\.wslconfig`); got != "~/.wslconfig" {
		t.Errorf("Short() = %q, want ~/.wslconfig", got)
	}

	// Files work with %USERPROFILE% as the home folder.
	e := newEnv(t)
	f := &file.File{Path: `~\.wslconfig`, Content: file.Bytes([]byte("[wsl2]\nmemory=16GB\n")), CRLF: true}
	if f.ID() != "file:~/.wslconfig" {
		t.Errorf("ID() = %q, want file:~/.wslconfig", f.ID())
	}
	e.apply(f)
	if got := e.read(".wslconfig"); got != "[wsl2]\r\nmemory=16GB\r\n" {
		t.Errorf("~/.wslconfig holds %q", got)
	}
}
