package shells_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/shells"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name.golden, or rewrites the file with
// -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (run the tests with -update to rewrite it):\n--- got\n%s--- want\n%s", path, got, want)
	}
}

// testEnv is the environment of a few modules, with something for every
// part of an environment file.
var testEnv = shells.Merge(
	shells.Env{Path: []string{"~/.local/bin"}},
	shells.Env{
		Path:      []string{"$GOPATH/bin", "~/.local/bin"},
		Variables: []shells.Variable{{Name: "GOPATH", Value: "~/go"}},
	},
	shells.Env{
		Variables: []shells.Variable{{Name: "GREETING", Value: "say \"hi\" to ${GOPATH}s, it costs $5 \\ `id`"}},
		Init: []shells.InitStep{
			{
				Name: "sdkman",
				Bash: "export SDKMAN_DIR=\"$HOME/.sdkman\"\n[ -s \"$SDKMAN_DIR/bin/sdkman-init.sh\" ] && source \"$SDKMAN_DIR/bin/sdkman-init.sh\"\n",
				Zsh:  "export SDKMAN_DIR=\"$HOME/.sdkman\"\n[ -s \"$SDKMAN_DIR/bin/sdkman-init.sh\" ] && source \"$SDKMAN_DIR/bin/sdkman-init.sh\"\n",
				Last: true,
			},
			{Name: "direnv", Bash: `eval "$(direnv hook bash)"`, Zsh: `eval "$(direnv hook zsh)"`, Fish: "direnv hook fish | source"},
			{Name: "atuin", Zsh: `eval "$(atuin init zsh)"`, Fish: "atuin init fish | source"},
		},
	},
)

// settings are a shell's own settings, with snippets in the shell's
// folder.
func settings(shell string, manageRC bool, omz *shells.OhMyZsh) shells.Config {
	dir := "~/.config/maisys/shell/" + shell + "/"
	return shells.Config{
		Env:         testEnv,
		Variables:   []shells.Variable{{Name: "EDITOR", Value: "nvim"}, {Name: "HIST_STAMPS", Value: "yyyy-mm-dd"}},
		PreSnippets: []string{dir + "first." + shell},
		Snippets:    []string{dir + "aliases." + shell, dir + "more aliases." + shell},
		ManageRC:    manageRC,
		OhMyZsh:     omz,
	}
}

var ohMyZsh = &shells.OhMyZsh{Theme: "aphrodite", Plugins: []string{"git", "zsh-autosuggestions"}}

func TestRender(t *testing.T) {
	bash, zsh, fish := shells.Bash{}, shells.Zsh{}, shells.Fish{}
	tests := []struct {
		name     string
		shell    shells.Shell
		cfg      shells.Config
		skipped  []string // the warnings RenderEnv gives
		rendered bool     // whether RenderRC writes an rc file
	}{
		{name: "bash", shell: bash, cfg: settings("bash", true, nil), skipped: []string{"atuin"}, rendered: true},
		{name: "bash-unmanaged", shell: bash, cfg: settings("bash", false, nil), skipped: []string{"atuin"}},
		{name: "bash-empty", shell: bash, cfg: shells.Config{ManageRC: true}, rendered: true},
		{name: "zsh", shell: zsh, cfg: settings("zsh", true, nil), rendered: true},
		{name: "zsh-oh-my-zsh", shell: zsh, cfg: settings("zsh", true, ohMyZsh), rendered: true},
		{name: "zsh-oh-my-zsh-unmanaged", shell: zsh, cfg: settings("zsh", false, ohMyZsh)},
		{name: "fish", shell: fish, cfg: settings("fish", false, nil), skipped: []string{"sdkman"}},
		{name: "fish-empty", shell: fish, cfg: shells.Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, warnings := tt.shell.RenderEnv(tt.cfg)
			golden(t, tt.name+".env", env)
			var skipped []string
			for _, w := range warnings {
				if w.Shell != tt.shell.Name() {
					t.Errorf("warning %q is for shell %q", w, w.Shell)
				}
				skipped = append(skipped, w.Step)
			}
			if !slices.Equal(skipped, tt.skipped) {
				t.Errorf("RenderEnv warns about %q, want %q", skipped, tt.skipped)
			}
			syntax(t, tt.shell.Name(), env)

			rc := tt.shell.RenderRC(tt.cfg)
			if tt.rendered {
				golden(t, tt.name+".rc", rc)
				syntax(t, tt.shell.Name(), rc)
			}
		})
	}
}

func TestWarning(t *testing.T) {
	w := shells.Warning{Shell: "fish", Step: "sdkman"}
	if got, want := w.String(), "fish: sdkman has no fish integration (skipped)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestShells(t *testing.T) {
	tests := []struct {
		name, binary, env, rc string
	}{
		{"bash", "/usr/bin/bash", "~/.config/maisys/shell/env.sh", "~/.bashrc"},
		{"zsh", "/usr/bin/zsh", "~/.config/maisys/shell/env.zsh", "~/.zshrc"},
		{"fish", "/usr/bin/fish", "~/.config/fish/conf.d/maisys.fish", ""},
	}
	all := shells.All()
	if len(all) != len(tests) {
		t.Fatalf("All() has %d shells, want %d", len(all), len(tests))
	}
	for i, tt := range tests {
		sh := all[i]
		if sh.Name() != tt.name || sh.Binary() != tt.binary || sh.EnvFile() != tt.env || sh.RCFile() != tt.rc {
			t.Errorf("shell %d = %s, %s, %s, %q; want %s, %s, %s, %q",
				i, sh.Name(), sh.Binary(), sh.EnvFile(), sh.RCFile(), tt.name, tt.binary, tt.env, tt.rc)
		}
		for _, osName := range config.LinuxOS() {
			if got := sh.Package(osName); got != tt.name {
				t.Errorf("%s.Package(%s) = %q, want %q", tt.name, osName, got, tt.name)
			}
		}
		if got := sh.Package(config.Windows); got != "" {
			t.Errorf("%s.Package(windows) = %q, want none", tt.name, got)
		}
	}
}

func TestMerge(t *testing.T) {
	a := shells.Env{
		Path:      []string{"~/.local/bin", "~/go/bin"},
		Variables: []shells.Variable{{Name: "A", Value: "1"}},
		Init:      []shells.InitStep{{Name: "a", Bash: "a"}},
	}
	b := shells.Env{
		Path:      []string{"~/go/bin", "~/.cargo/bin"},
		Variables: []shells.Variable{{Name: "B", Value: "2"}},
		Init:      []shells.InitStep{{Name: "b", Bash: "b", Last: true}},
	}
	want := shells.Env{
		Path:      []string{"~/.local/bin", "~/go/bin", "~/.cargo/bin"},
		Variables: []shells.Variable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
		Init:      []shells.InitStep{{Name: "a", Bash: "a"}, {Name: "b", Bash: "b", Last: true}},
	}
	if got := shells.Merge(a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("Merge() = %+v, want %+v", got, want)
	}
	if got := shells.Merge(); !reflect.DeepEqual(got, shells.Env{}) {
		t.Errorf("Merge() = %+v, want nothing", got)
	}
}

func TestEnvCheck(t *testing.T) {
	tests := []struct {
		name string
		env  shells.Env
		want []string
	}{
		{name: "valid", env: testEnv},
		{name: "empty", env: shells.Env{}},
		{
			name: "invalid",
			env: shells.Env{
				Path:      []string{"", "/a:/b"},
				Variables: []shells.Variable{{Name: "1X"}, {Name: "A-B"}, {Name: "OK"}},
				Init:      []shells.InitStep{{Name: " ", Bash: "true"}},
			},
			want: []string{
				`"" can't be added to PATH`,
				`"/a:/b" can't be added to PATH`,
				`"1X" isn't a valid variable name`,
				`"A-B" isn't a valid variable name`,
				"an init step has no name",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.env.Check()
			var got []string
			if err != nil {
				got = strings.Split(err.Error(), "\n")
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
		})
	}
}

// syntax checks that the shell can parse script, if the shell is
// installed.
func syntax(t *testing.T, shell string, script []byte) {
	t.Helper()
	bin := lookShell(t, shell)
	if bin == "" {
		return
	}
	cmd := exec.CommandContext(t.Context(), bin, "-n")
	if shell == "fish" {
		cmd = exec.CommandContext(t.Context(), bin, "--no-execute")
	}
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s can't parse the file: %v\n%s", shell, err, out)
	}
}

// lookShell returns the path of the shell, or "" if it isn't installed.
func lookShell(t *testing.T, shell string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return ""
	}
	bin, err := exec.LookPath(shell)
	if errors.Is(err, exec.ErrNotFound) {
		return ""
	} else if err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestSource runs each installed shell with its environment file, sourced
// twice, and checks what it sets: PATH has each folder once, in order, in
// front of what it had; variables hold their values; and init steps run in
// order, with those marked Last at the end.
func TestSource(t *testing.T) {
	env := shells.Env{
		Path: []string{"~/.local/bin", "$GOPATH/bin", "/usr/bin"},
		Variables: []shells.Variable{
			{Name: "GOPATH", Value: "~/go"},
			{Name: "TRICKY", Value: "say \"hi\" to ${GOPATH}s, it costs $5 \\ `id` $(id) 'q' [1] ~/x"},
		},
		Init: []shells.InitStep{
			{Name: "last", Bash: "STEPS=${STEPS}c", Zsh: "STEPS=${STEPS}c", Fish: "set -g STEPS {$STEPS}c", Last: true},
			{Name: "first", Bash: "STEPS=${STEPS}a", Zsh: "STEPS=${STEPS}a", Fish: "set -g STEPS {$STEPS}a"},
			{Name: "second", Bash: "STEPS=${STEPS}b", Zsh: "STEPS=${STEPS}b", Fish: "set -g STEPS {$STEPS}b"},
		},
	}
	for _, sh := range shells.All() {
		t.Run(sh.Name(), func(t *testing.T) {
			bin := lookShell(t, sh.Name())
			if bin == "" {
				t.Skipf("%s isn't installed", sh.Name())
			}
			home := t.TempDir()
			data, _ := sh.RenderEnv(shells.Config{Env: env, ManageRC: true})
			file := filepath.Join(home, "env")
			if err := os.WriteFile(file, data, 0o644); err != nil {
				t.Fatal(err)
			}

			var cmd *exec.Cmd
			switch sh.Name() {
			case "fish":
				script := "source '" + file + "'; source '" + file + "'; string join : $PATH; echo $TRICKY; echo $STEPS"
				cmd = exec.CommandContext(t.Context(), bin, "--no-config", "-c", script)
			case "zsh":
				script := `source "$1"; source "$1"; printf '%s\n' "$PATH" "$TRICKY" "$STEPS"`
				cmd = exec.CommandContext(t.Context(), bin, "-f", "-c", script, "zsh", file)
			default:
				script := `source "$1"; source "$1"; printf '%s\n' "$PATH" "$TRICKY" "$STEPS"`
				cmd = exec.CommandContext(t.Context(), bin, "--norc", "--noprofile", "-c", script, "bash", file)
			}
			cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/bin", "STEPS="}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v\n%s", sh.Name(), err, out)
			}
			want := strings.Join([]string{
				home + "/.local/bin:" + home + "/go/bin:/usr/bin:/usr/local/bin:/bin",
				"say \"hi\" to " + home + "/gos, it costs $5 \\ `id` $(id) 'q' [1] ~/x",
				"abcabc",
			}, "\n") + "\n"
			if string(out) != want {
				t.Errorf("%s printed:\n%s\nwant:\n%s", sh.Name(), out, want)
			}
		})
	}
}
