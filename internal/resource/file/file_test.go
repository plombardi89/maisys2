package file_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// textFile returns a file in the home folder holding content.
func textFile(rel, content string) *file.File {
	return &file.File{Path: "~/" + rel, Content: file.Bytes([]byte(content))}
}

// backups returns the backups of a file in the home folder.
func backups(t *testing.T, e *env, rel string) []string {
	t.Helper()
	matches, err := filepath.Glob(e.path(rel) + ".maisys-bak.*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestFileCheckAndApply(t *testing.T) {
	const rel = ".config/nvim/init.lua"
	tests := []struct {
		name string
		// setup prepares the home folder and the state file.
		setup    func(e *env)
		action   resource.Action
		summary  string
		backup   string // what applying backs up, if anything
		tookOver bool
	}{
		{
			name:    "missing",
			setup:   func(*env) {},
			action:  resource.Create,
			summary: "~/.config/nvim/init.lua",
		},
		{
			name: "applied before and unchanged",
			setup: func(e *env) {
				e.apply(textFile(rel, "old\n"))
			},
			action:  resource.Update,
			summary: "~/.config/nvim/init.lua",
		},
		{
			name: "changed outside maisys",
			setup: func(e *env) {
				e.apply(textFile(rel, "old\n"))
				e.write(rel, "edited\n", 0o644)
			},
			action:  resource.Warn,
			summary: "~/.config/nvim/init.lua changed outside maisys (will back up and overwrite)",
			backup:  "edited\n",
		},
		{
			name: "there before maisys",
			setup: func(e *env) {
				e.write(rel, "mine\n", 0o644)
			},
			action:   resource.Warn,
			summary:  "~/.config/nvim/init.lua exists (will back up and take over)",
			backup:   "mine\n",
			tookOver: true,
		},
		{
			name: "taken over before and unchanged",
			setup: func(e *env) {
				e.write(rel, "mine\n", 0o644)
				e.apply(textFile(rel, "old\n"))
			},
			action:   resource.Update,
			summary:  "~/.config/nvim/init.lua",
			tookOver: true,
		},
		{
			name: "taken over before, then deleted",
			setup: func(e *env) {
				e.write(rel, "mine\n", 0o644)
				e.apply(textFile(rel, "old\n"))
				if err := os.Remove(e.path(rel)); err != nil {
					e.t.Fatal(err)
				}
			},
			action:   resource.Create,
			summary:  "~/.config/nvim/init.lua",
			tookOver: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			before := backups(t, e, rel)

			f := textFile(rel, "new\n")
			c, rec := e.apply(f)
			if c.Action != tt.action || c.Summary != tt.summary {
				t.Errorf("Check() = %s %q, want %s %q", c.Action, c.Summary, tt.action, tt.summary)
			}
			if got := e.read(rel); got != "new\n" {
				t.Errorf("the file holds %q after Apply(), want %q", got, "new\n")
			}
			if rec.Details[file.DetailHash] != sha("new\n") || rec.TookOver != tt.tookOver || !slices.Equal(rec.Paths, []string{"~/.config/nvim/init.lua"}) {
				t.Errorf("record = %+v, want the hash of what was written, took over %t, and the path", rec, tt.tookOver)
			}

			got := slices.DeleteFunc(backups(t, e, rel), func(name string) bool { return slices.Contains(before, name) })
			switch {
			case tt.backup == "" && len(got) > 0:
				t.Errorf("backups = %q, want none", got)
			case tt.backup != "":
				if want := []string{e.path(rel) + stamp}; !slices.Equal(got, want) {
					t.Fatalf("backups = %q, want %q", got, want)
				}
				if data, err := os.ReadFile(got[0]); err != nil || string(data) != tt.backup {
					t.Errorf("the backup holds %q, %v; want %q", data, err, tt.backup)
				}
			}

			if c := e.check(f); c.Action != resource.NoChange {
				t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
			}
		})
	}
}

func TestFileNoChange(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		setup func(e *env)
	}{
		{"there before maisys with the same contents", "want\n", func(e *env) {
			e.write(".gitconfig", "want\n", 0o644)
		}},
		{"applied before", "want\n", func(e *env) {
			e.apply(textFile(".gitconfig", "want\n"))
		}},
		{"changed outside maisys to what the config wants", "want\n", func(e *env) {
			e.apply(textFile(".gitconfig", "old\n"))
			e.write(".gitconfig", "want\n", 0o644)
		}},
		{"empty", "", func(e *env) {
			e.apply(textFile(".gitconfig", ""))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			if c := e.check(textFile(".gitconfig", tt.want)); c.Action != resource.NoChange || c.Summary != "" {
				t.Errorf("Check() = %s %q, want no change", c.Action, c.Summary)
			}
		})
	}
}

// TestFileRecord checks the record a check says a file should have when it
// already holds what it should: none while the state file's record is up
// to date, and otherwise the one applying would leave.
func TestFileRecord(t *testing.T) {
	const rel = ".config/foo"
	tests := []struct {
		name  string
		setup func(e *env)
		// want is the record the check says the file should have, if any.
		want *state.Record
	}{
		{
			name:  "applied before",
			setup: func(e *env) { e.apply(textFile(rel, "v2\n")) },
		},
		{
			name: "edited to what the config now says",
			setup: func(e *env) {
				e.apply(textFile(rel, "v1\n"))
				e.write(rel, "v2\n", 0o644)
			},
			want: &state.Record{Paths: []string{"~/.config/foo"}, Details: map[string]string{file.DetailHash: sha("v2\n")}},
		},
		{
			name:  "there before maisys with the same contents",
			setup: func(e *env) { e.write(rel, "v2\n", 0o644) },
			want:  &state.Record{Paths: []string{"~/.config/foo"}, Details: map[string]string{file.DetailHash: sha("v2\n")}, TookOver: true},
		},
		{
			name: "taken over, then edited to what the config now says",
			setup: func(e *env) {
				e.write(rel, "mine\n", 0o644)
				e.apply(textFile(rel, "v1\n"))
				e.write(rel, "v2\n", 0o644)
			},
			want: &state.Record{Paths: []string{"~/.config/foo"}, Details: map[string]string{file.DetailHash: sha("v2\n")}, TookOver: true},
		},
		{
			name: "a record without what Apply records",
			setup: func(e *env) {
				e.apply(textFile(rel, "v2\n"))
				e.state.Put(state.Record{ID: "file:~/.config/foo", Kind: "file", Module: "files"})
			},
			want: &state.Record{Paths: []string{"~/.config/foo"}, Details: map[string]string{file.DetailHash: sha("v2\n")}, TookOver: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			f := textFile(rel, "v2\n")
			c := e.check(f)
			if c.Action != resource.NoChange || c.Summary != "" {
				t.Errorf("Check() = %s %q, want no change", c.Action, c.Summary)
			}
			switch {
			case tt.want == nil && c.Record != nil:
				t.Errorf("Check() has the record %+v, want none: the state file's is up to date", *c.Record)
			case tt.want != nil && c.Record == nil:
				t.Errorf("Check() has no record, want %+v", *tt.want)
			case tt.want != nil && !reflect.DeepEqual(*c.Record, *tt.want):
				t.Errorf("Check() has the record %+v, want %+v", *c.Record, *tt.want)
			}

			// Once the record is saved, it's up to date.
			e.apply(f)
			if c := e.check(f); c.Action != resource.NoChange || c.Record != nil {
				t.Errorf("Check() after the record was saved = %s %q with record %v, want no change and no record", c.Action, c.Summary, c.Record)
			}
		})
	}
}

func TestFileCheckErrors(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path(".config/nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		f    *file.File
		want string
	}{
		{"a folder", textFile(".config/nvim", "x"), "~/.config/nvim is a folder, not a file"},
		{"a relative path", &file.File{Path: "notes.txt", Content: file.Bytes(nil)}, `"notes.txt" must start with ~/ or be an absolute path`},
		{"outside the home folder", &file.File{Path: "~/../x", Content: file.Bytes(nil)}, `"~/../x" must be inside your home folder`},
		{"no content", &file.File{Path: "~/x"}, "~/x has no content"},
		{"content that fails", &file.File{Path: "~/x", Content: func(*resource.Runtime) ([]byte, error) {
			return nil, errors.New("no such variable")
		}}, "no such variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.f.Check(t.Context(), e.rt); err == nil || err.Error() != tt.want {
				t.Errorf("Check() error = %v, want %q", err, tt.want)
			}
			if _, err := tt.f.Apply(t.Context(), e.rt); err == nil || err.Error() != tt.want {
				t.Errorf("Apply() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFileID(t *testing.T) {
	tests := []struct {
		f    *file.File
		id   string
		path string
	}{
		{&file.File{Path: "~/.gitconfig"}, "file:~/.gitconfig", "~/.gitconfig"},
		{&file.File{Path: "~/.config//nvim/./init.lua"}, "file:~/.config/nvim/init.lua", "~/.config/nvim/init.lua"},
		{&file.File{Path: "~/x", Key: "systemd-unit:user/x.service"}, "systemd-unit:user/x.service", "~/x"},
	}
	for _, tt := range tests {
		if got := tt.f.ID(); got != tt.id {
			t.Errorf("ID() = %q, want %q", got, tt.id)
		}
		if got := tt.f.Targets(); !slices.Equal(got, []string{tt.path}) {
			t.Errorf("Targets() = %q, want %q", got, tt.path)
		}
		if tt.f.Kind() != "file" || tt.f.Privileged() {
			t.Errorf("Kind() = %q, Privileged() = %t; want file, false", tt.f.Kind(), tt.f.Privileged())
		}
	}
}

func TestFileBackupNames(t *testing.T) {
	e := newEnv(t)
	e.write(".gitconfig", "mine\n", 0o600)
	first, err := file.Backup(e.path(".gitconfig"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := file.Backup(e.path(".gitconfig"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := e.path(".gitconfig") + stamp; first != want || second != want+".1" {
		t.Errorf("backups = %q and %q, want %q and %q", first, second, want, want+".1")
	}
	for _, name := range []string{first, second} {
		if data, err := os.ReadFile(name); err != nil || string(data) != "mine\n" {
			t.Errorf("%s holds %q, %v; want the file's contents", name, data, err)
		}
	}
	if _, err := file.Backup(e.path("missing"), testNow); err == nil {
		t.Error("Backup() of a missing file: no error")
	}
}

func TestFileWritesAtomically(t *testing.T) {
	e := newEnv(t)
	e.write(".config/nvim/other.lua", "not maisys's\n", 0o644)
	e.apply(textFile(".config/nvim/init.lua", "new\n"))

	entries, err := os.ReadDir(e.path(".config/nvim"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if want := []string{"init.lua", "other.lua"}; !slices.Equal(names, want) {
		t.Errorf("the folder holds %q, want %q and no temporary files", names, want)
	}

	// A file that fails to render isn't touched.
	failing := &file.File{Path: "~/.config/nvim/init.lua", Content: func(*resource.Runtime) ([]byte, error) {
		return nil, errors.New("broken template")
	}}
	if _, err := failing.Apply(t.Context(), e.rt); err == nil {
		t.Error("Apply() with content that fails: no error")
	}
	if got := e.read(".config/nvim/init.lua"); got != "new\n" {
		t.Errorf("the file holds %q, want it untouched", got)
	}

	// A file can't be written where a file is in the way of its folder.
	blocked := textFile("other.lua/x", "x")
	e.write("other.lua", "", 0o644)
	if _, err := blocked.Apply(t.Context(), e.rt); err == nil {
		t.Error("Apply() under a file: no error")
	}
}

func TestFileReplacesLink(t *testing.T) {
	skipOnWindows(t, "symlinks without developer mode")
	e := newEnv(t)
	e.write("dotfiles/gitconfig", "mine\n", 0o644)
	if err := os.Symlink(e.path("dotfiles/gitconfig"), e.path(".gitconfig")); err != nil {
		t.Fatal(err)
	}

	c, rec := e.apply(textFile(".gitconfig", "new\n"))
	if c.Action != resource.Warn || c.Summary != "~/.gitconfig exists (will back up and take over)" || !rec.TookOver {
		t.Errorf("Check() = %s %q, took over %t; want the link taken over", c.Action, c.Summary, rec.TookOver)
	}
	if info, err := os.Lstat(e.path(".gitconfig")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("~/.gitconfig is %v, %v; want a file", info, err)
	}
	if got := e.read("dotfiles/gitconfig"); got != "mine\n" {
		t.Errorf("the link's target holds %q, want it untouched", got)
	}
	if target, err := os.Readlink(e.path(".gitconfig") + stamp); err != nil || target != e.path("dotfiles/gitconfig") {
		t.Errorf("the backup points to %q, %v; want the link backed up as a link", target, err)
	}

	// A link to a file with the right contents is left alone.
	e = newEnv(t)
	e.write("dotfiles/gitconfig", "new\n", 0o644)
	if err := os.Symlink(e.path("dotfiles/gitconfig"), e.path(".gitconfig")); err != nil {
		t.Fatal(err)
	}
	if c := e.check(textFile(".gitconfig", "new\n")); c.Action != resource.NoChange {
		t.Errorf("Check() of a link to the right contents = %s %q, want no change", c.Action, c.Summary)
	}

	// A link that points nowhere is backed up and replaced.
	e = newEnv(t)
	if err := os.Symlink(e.path("gone"), e.path(".gitconfig")); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.apply(textFile(".gitconfig", "new\n")); c.Action != resource.Warn {
		t.Errorf("Check() of a broken link = %s %q, want it taken over", c.Action, c.Summary)
	}
	if got := e.read(".gitconfig"); got != "new\n" {
		t.Errorf("~/.gitconfig holds %q, want %q", got, "new\n")
	}
}

func TestFileModes(t *testing.T) {
	skipOnWindows(t, "permission bits")
	tests := []struct {
		name    string
		before  os.FileMode // the file there before, if any
		mode    os.FileMode
		exec    bool
		want    os.FileMode
		summary string // the check's summary, if the contents are the same
	}{
		{name: "new file", want: 0o644},
		{name: "new executable", exec: true, want: 0o755},
		{name: "new file with a mode", mode: 0o600, want: 0o600},
		{name: "a mode, executable or not", mode: 0o700, exec: false, want: 0o700},
		{name: "keeps permissions", before: 0o640, want: 0o640},
		{name: "adds the executable bits", before: 0o640, exec: true, want: 0o750, summary: "~/bin/tool (mode 0640 → 0750)"},
		{name: "removes the executable bits", before: 0o755, want: 0o644, summary: "~/bin/tool (mode 0755 → 0644)"},
		{name: "sets the mode", before: 0o644, mode: 0o600, want: 0o600, summary: "~/bin/tool (mode 0644 → 0600)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.before != 0 {
				e.write("bin/tool", "x\n", tt.before)
			}
			f := &file.File{Path: "~/bin/tool", Content: file.Bytes([]byte("x\n")), Mode: tt.mode, Executable: tt.exec}
			c := e.check(f)
			switch {
			case tt.before == 0 && c.Action != resource.Create:
				t.Errorf("Check() = %s %q, want create", c.Action, c.Summary)
			case tt.before != 0 && tt.summary == "" && c.Action != resource.NoChange:
				t.Errorf("Check() = %s %q, want no change", c.Action, c.Summary)
			case tt.summary != "" && (c.Action != resource.Update || c.Summary != tt.summary):
				t.Errorf("Check() = %s %q, want update %q", c.Action, c.Summary, tt.summary)
			}
			if c.Action != resource.NoChange {
				e.apply(f)
			}
			if got := e.perm("bin/tool"); got != tt.want {
				t.Errorf("mode = %04o, want %04o", got, tt.want)
			}
			if c := e.check(f); c.Action != resource.NoChange {
				t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
			}
		})
	}
}

func TestFileFolders(t *testing.T) {
	e := newEnv(t)
	e.apply(&file.File{Path: "~/.ssh/config", Content: file.Bytes([]byte("Host *\n")), Mode: 0o600})
	e.apply(textFile(".config/nvim/lua/init.lua", "x"))
	if got := e.read(".config/nvim/lua/init.lua"); got != "x" {
		t.Errorf("the file in new folders holds %q, want %q", got, "x")
	}
	if perm := e.perm(".ssh"); runtime.GOOS != "windows" && perm != 0o700 {
		t.Errorf("~/.ssh has mode %04o, want 0700", perm)
	}
}

func TestFileCRLF(t *testing.T) {
	e := newEnv(t)
	f := &file.File{Path: "~/AppData/notes.txt", Content: file.Bytes([]byte("a\nb\r\nc\n")), CRLF: true}
	_, rec := e.apply(f)
	if got := e.read("AppData/notes.txt"); got != "a\r\nb\r\nc\r\n" {
		t.Errorf("the file holds %q, want CRLF line endings", got)
	}
	if rec.Details[file.DetailHash] != sha("a\r\nb\r\nc\r\n") {
		t.Errorf("the recorded hash isn't the hash of what was written")
	}
	if c := e.check(f); c.Action != resource.NoChange {
		t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
	}
}

func TestFileRoot(t *testing.T) {
	skipOnWindows(t, "sudo")
	e := newEnv(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "wsl.conf")
	e.runner.On("install", installFake(t))
	f := &file.File{Path: conf, Content: file.Bytes([]byte("[boot]\nsystemd=true\n")), Root: true}
	if !f.Privileged() || f.ID() != "file:"+conf {
		t.Errorf("Privileged() = %t, ID() = %q; want true and file:%s", f.Privileged(), f.ID(), conf)
	}

	if err := os.WriteFile(conf, []byte("[boot]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, rec := e.apply(f)
	if c.Action != resource.Warn || c.Summary != conf+" exists (will back up and take over)" {
		t.Errorf("Check() = %s %q, want the file taken over", c.Action, c.Summary)
	}
	if rec.Details[file.DetailRoot] != "true" || !rec.TookOver {
		t.Errorf("record = %+v, want it written as root and taken over", rec)
	}
	if data, err := os.ReadFile(conf); err != nil || string(data) != "[boot]\nsystemd=true\n" {
		t.Errorf("the file holds %q, %v; want the new contents", data, err)
	}
	if data, err := os.ReadFile(conf + stamp); err != nil || string(data) != "[boot]\n" {
		t.Errorf("the backup holds %q, %v; want the old contents", data, err)
	}
	cmds := e.runner.RootCommands()
	if len(cmds) != 2 || !strings.HasSuffix(cmds[0], " "+conf+stamp) || !strings.HasSuffix(cmds[1], " "+conf) {
		t.Errorf("commands run as root = %q, want the backup and the file installed", cmds)
	}
	if c := e.check(f); c.Action != resource.NoChange {
		t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
	}
}
