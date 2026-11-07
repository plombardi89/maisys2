package settings

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fakeEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// pathsMap lists every path Paths gives, for comparing.
func pathsMap(p Paths) map[string]string {
	return map[string]string{
		"Home":            p.Home,
		"ConfigDir":       p.ConfigDir,
		"StateDir":        p.StateDir,
		"CacheDir":        p.CacheDir,
		"SettingsFile":    p.SettingsFile(),
		"CredentialsFile": p.CredentialsFile(),
		"DefaultRepoDir":  p.DefaultRepoDir(),
		"StateFile":       p.StateFile(),
		"LockFile":        p.LockFile(),
		"LogsDir":         p.LogsDir(),
	}
}

func TestDefaultPaths(t *testing.T) {
	windowsEnv := map[string]string{
		"USERPROFILE":  `C:\Users\phil`,
		"APPDATA":      `C:\Users\phil\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\phil\AppData\Local`,
		"HOME":         "/ignored",
	}
	windowsWant := map[string]string{
		"Home":            `C:\Users\phil`,
		"ConfigDir":       `C:\Users\phil\AppData\Roaming\maisys`,
		"StateDir":        `C:\Users\phil\AppData\Local\maisys`,
		"CacheDir":        `C:\Users\phil\AppData\Local\maisys\cache`,
		"SettingsFile":    `C:\Users\phil\AppData\Roaming\maisys\maisys.toml`,
		"CredentialsFile": `C:\Users\phil\AppData\Roaming\maisys\git-credentials`,
		"DefaultRepoDir":  `C:\Users\phil\maisys-config`,
		"StateFile":       `C:\Users\phil\AppData\Local\maisys\state.json`,
		"LockFile":        `C:\Users\phil\AppData\Local\maisys\lock`,
		"LogsDir":         `C:\Users\phil\AppData\Local\maisys\logs`,
	}
	withTrailingSlashes := map[string]string{}
	for k, v := range windowsEnv {
		withTrailingSlashes[k] = v + `\`
	}

	tests := []struct {
		name string
		goos string
		env  map[string]string
		want map[string]string
	}{
		{
			name: "linux",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/phil", "APPDATA": "ignored"},
			want: map[string]string{
				"Home":            "/home/phil",
				"ConfigDir":       "/home/phil/.config/maisys",
				"StateDir":        "/home/phil/.local/state/maisys",
				"CacheDir":        "/home/phil/.cache/maisys",
				"SettingsFile":    "/home/phil/.config/maisys/maisys.toml",
				"CredentialsFile": "/home/phil/.config/maisys/git-credentials",
				"DefaultRepoDir":  "/home/phil/maisys-config",
				"StateFile":       "/home/phil/.local/state/maisys/state.json",
				"LockFile":        "/home/phil/.local/state/maisys/lock",
				"LogsDir":         "/home/phil/.local/state/maisys/logs",
			},
		},
		{
			name: "linux with XDG folders",
			goos: "linux",
			env: map[string]string{
				"HOME":            "/home/phil/",
				"XDG_CONFIG_HOME": "/xdg/config",
				"XDG_STATE_HOME":  "/xdg/state/",
				"XDG_CACHE_HOME":  "/xdg/cache",
			},
			want: map[string]string{
				"Home":            "/home/phil/",
				"ConfigDir":       "/xdg/config/maisys",
				"StateDir":        "/xdg/state/maisys",
				"CacheDir":        "/xdg/cache/maisys",
				"SettingsFile":    "/xdg/config/maisys/maisys.toml",
				"CredentialsFile": "/xdg/config/maisys/git-credentials",
				"DefaultRepoDir":  "/home/phil/maisys-config",
				"StateFile":       "/xdg/state/maisys/state.json",
				"LockFile":        "/xdg/state/maisys/lock",
				"LogsDir":         "/xdg/state/maisys/logs",
			},
		},
		{
			name: "windows",
			goos: "windows",
			env:  windowsEnv,
			want: windowsWant,
		},
		{
			name: "windows with trailing backslashes",
			goos: "windows",
			env:  withTrailingSlashes,
			want: func() map[string]string {
				want := maps.Clone(windowsWant)
				want["Home"] = `C:\Users\phil\`
				return want
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := DefaultPaths(tt.goos, fakeEnv(tt.env))
			if err != nil {
				t.Fatalf("DefaultPaths: %v", err)
			}
			if got := pathsMap(p); !reflect.DeepEqual(got, tt.want) {
				for k, want := range tt.want {
					if got[k] != want {
						t.Errorf("%s = %q, want %q", k, got[k], want)
					}
				}
			}
		})
	}
}

func TestDefaultPathsMissingVariables(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "linux without HOME",
			goos: "linux",
			env: map[string]string{
				"XDG_CONFIG_HOME": "/xdg/config",
				"XDG_STATE_HOME":  "/xdg/state",
				"XDG_CACHE_HOME":  "/xdg/cache",
			},
			want: "can't find where to keep maisys's files: the HOME environment variable isn't set",
		},
		{
			name: "windows without USERPROFILE",
			goos: "windows",
			env:  map[string]string{"APPDATA": `C:\a`, "LOCALAPPDATA": `C:\l`},
			want: "can't find where to keep maisys's files: the USERPROFILE environment variable isn't set",
		},
		{
			name: "windows without anything",
			goos: "windows",
			env:  map[string]string{"HOME": "/home/phil"},
			want: "can't find where to keep maisys's files: the USERPROFILE, APPDATA and LOCALAPPDATA environment variables aren't set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DefaultPaths(tt.goos, fakeEnv(tt.env))
			if err == nil || err.Error() != tt.want {
				t.Errorf("DefaultPaths error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPathsUseTheRunningOSByDefault(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Home: dir, ConfigDir: dir, StateDir: dir}
	if got, want := p.SettingsFile(), filepath.Join(dir, "maisys.toml"); got != want {
		t.Errorf("SettingsFile = %q, want %q", got, want)
	}
	if got, want := p.DefaultRepoDir(), filepath.Join(dir, "maisys-config"); got != want {
		t.Errorf("DefaultRepoDir = %q, want %q", got, want)
	}
}

func TestSaveAndLoad(t *testing.T) {
	tests := []Settings{
		{RepoDir: "/home/phil/maisys-config", RepoURL: "https://github.com/plombardi89/maisys-config.git", Machine: "phil-workp14s"},
		{RepoDir: `C:\Users\phil\maisys-config`, RepoURL: "git@github.com:plombardi89/maisys-config.git", Machine: "workbox-camwin"},
		{RepoDir: "/home/phil/it's \"quoted\"", Machine: "no-remote"},
	}
	for _, want := range tests {
		t.Run(want.Machine, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config", "maisys")
			path := filepath.Join(dir, "maisys.toml")
			s := want
			if err := s.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if s.FormatVersion != FormatVersion {
				t.Errorf("FormatVersion after Save = %d, want %d", s.FormatVersion, FormatVersion)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want.FormatVersion = FormatVersion
			if *got != want {
				t.Errorf("Load = %+v, want %+v", *got, want)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), header) {
				t.Errorf("file doesn't start with the header:\n%s", data)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("folder holds %v, want only maisys.toml", entries)
			}
			if runtime.GOOS != "windows" {
				checkMode(t, path, 0o600)
				checkMode(t, dir, 0o700|fs.ModeDir)
			}
		})
	}
}

func checkMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Errorf("%s has mode %v, want %v", path, info.Mode(), want)
	}
}

func TestSaveReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maisys.toml")
	first := &Settings{RepoDir: "/one", Machine: "one"}
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	second := &Settings{RepoDir: "/two", Machine: "two"}
	if err := second.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *second {
		t.Errorf("Load = %+v, want %+v", *got, *second)
	}
}

func TestLoadMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maisys.toml")
	_, err := Load(path)
	if !errors.Is(err, ErrNoSettings) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load error = %v, want ErrNoSettings and fs.ErrNotExist", err)
	}
	if want := "no local settings: " + path + " doesn't exist"; err.Error() != want {
		t.Errorf("Load error = %q, want %q", err, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // after the file's path
	}{
		{
			name:    "unknown keys",
			content: "format_version = 1\nrepo_dir = '/x'\ncolor = 'blue'\n[extra]\nkey = 1\n",
			want:    ":3:1: unknown key color\n%s:4:2: unknown key extra",
		},
		{
			name:    "wrong type",
			content: "format_version = 1\nmachine = 3\n",
			want:    ":2:11: machine must be a string in quotes, not a TOML integer",
		},
		{
			name:    "format_version of the wrong type",
			content: "format_version = '1'\n",
			want:    ":1:18: format_version must be a whole number, not a TOML string",
		},
		{
			name:    "missing format_version",
			content: "repo_dir = '/x'\nmachine = 'box'\n",
			want:    ": format_version is missing",
		},
		{
			name:    "newer format",
			content: "format_version = 2\nsomething_new = true\n",
			want:    " was written by a newer version of maisys (format_version 2; this version reads up to 1): update maisys and try again",
		},
		{
			name:    "unknown older format",
			content: "format_version = 0\n",
			want:    ": format_version 0 isn't a format maisys knows",
		},
		{
			name:    "not TOML",
			content: "format_version = 1\nmachine =\n",
			want:    ":2:10: unexpected character U+000A at start of value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "maisys.toml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			want := path + strings.ReplaceAll(tt.want, "%s", path)
			if err == nil || err.Error() != want {
				t.Errorf("Load error = %v\nwant %s", err, want)
			}
		})
	}
}

// testSteps are fake migrations from format 1 to 2 (repo becomes repo_dir)
// and 2 to 3 (host becomes machine). Each records that it ran in ran.
func testSteps(ran *[]int) map[int]migration {
	rename := func(from, to string) func(map[string]any) error {
		return func(table map[string]any) error {
			if v, ok := table[from]; ok {
				table[to] = v
				delete(table, from)
			}
			return nil
		}
	}
	return map[int]migration{
		1: func(table map[string]any) error {
			*ran = append(*ran, 1)
			return rename("repo", "repo_dir")(table)
		},
		2: func(table map[string]any) error {
			*ran = append(*ran, 2)
			return rename("host", "machine")(table)
		},
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMigrate(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		oldBackup    string // an existing backup, if not ""
		wantSteps    []int
		wantBackup   string
		wantSettings Settings
	}{
		{
			name:         "from format 1",
			content:      "# old\nformat_version = 1\nrepo = '/x'\nrepo_url = 'https://example.com/c.git'\nhost = 'box'\n",
			wantSteps:    []int{1, 2},
			wantBackup:   "maisys.toml.v1.bak",
			wantSettings: Settings{FormatVersion: 3, RepoDir: "/x", RepoURL: "https://example.com/c.git", Machine: "box"},
		},
		{
			name:         "from format 2, keeping an existing backup",
			content:      "format_version = 2\nrepo_dir = '/x'\nhost = 'box'\n",
			oldBackup:    "an older backup",
			wantSteps:    []int{2},
			wantBackup:   "maisys.toml.v2.bak",
			wantSettings: Settings{FormatVersion: 3, RepoDir: "/x", Machine: "box"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "maisys.toml")
			writeTestFile(t, path, tt.content)
			backup := filepath.Join(dir, tt.wantBackup)
			if tt.oldBackup != "" {
				writeTestFile(t, backup, tt.oldBackup)
			}

			var ran []int
			got, err := load(path, 3, testSteps(&ran))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if *got != tt.wantSettings {
				t.Errorf("load = %+v, want %+v", *got, tt.wantSettings)
			}
			if !reflect.DeepEqual(ran, tt.wantSteps) {
				t.Errorf("migrations run = %v, want %v", ran, tt.wantSteps)
			}

			wantBackup := tt.content
			if tt.oldBackup != "" {
				wantBackup = tt.oldBackup
			}
			if got := readTestFile(t, backup); got != wantBackup {
				t.Errorf("backup = %q, want %q", got, wantBackup)
			}

			// The migrated file is saved in the new format.
			ran = nil
			again, err := load(path, 3, testSteps(&ran))
			if err != nil {
				t.Fatalf("loading the migrated file: %v", err)
			}
			if *again != tt.wantSettings || len(ran) > 0 {
				t.Errorf("loading the migrated file = %+v after migrations %v, want %+v and none", *again, ran, tt.wantSettings)
			}
			if !strings.HasPrefix(readTestFile(t, path), header) {
				t.Error("migrated file doesn't start with the header")
			}
		})
	}
}

func TestMigrateErrors(t *testing.T) {
	failing := errors.New("broken")
	tests := []struct {
		name    string
		content string
		steps   map[int]migration
		want    string // after the file's path
	}{
		{
			name:    "a migration fails",
			content: "format_version = 1\n",
			steps:   map[int]migration{1: func(map[string]any) error { return failing }},
			want:    ": migrating from format_version 1 to 2: broken",
		},
		{
			name:    "a migration is missing",
			content: "format_version = 1\n",
			steps:   map[int]migration{1: func(map[string]any) error { return nil }},
			want:    ": format_version 1 isn't a format maisys knows",
		},
		{
			name:    "unknown keys after migrating",
			content: "format_version = 1\nrepo_dir = '/x'\nleft_over = 1\n",
			steps: map[int]migration{
				1: func(map[string]any) error { return nil },
				2: func(map[string]any) error { return nil },
			},
			want: ", migrated to format_version 3: unknown key left_over",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "maisys.toml")
			writeTestFile(t, path, tt.content)

			_, err := load(path, 3, tt.steps)
			if want := path + tt.want; err == nil || err.Error() != want {
				t.Errorf("load error = %v\nwant %s", err, want)
			}
			if got := readTestFile(t, path); got != tt.content {
				t.Errorf("file changed to %q", got)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("folder holds %v, want only maisys.toml", entries)
			}
		})
	}
}

func TestEveryOlderFormatHasAMigration(t *testing.T) {
	for v := 1; v < FormatVersion; v++ {
		if migrations[v] == nil {
			t.Errorf("no migration from format_version %d to %d", v, v+1)
		}
	}
}
