package file_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
)

func TestTemplate(t *testing.T) {
	e := newEnv(t)
	t.Setenv("MAISYS_TEST_SECRET", "hunter2")
	vars := map[string]string{"email": "phil@example.com"}
	tests := []struct {
		name, text, want, err string
	}{
		{
			name: "vars, facts and machine",
			text: "email = {{.Vars.email}}\n# {{.Machine.Name}}: {{.Facts.OS}} {{.Facts.OSVersion}} on {{.Facts.Arch}}\n",
			want: "email = phil@example.com\n# box: ubuntu 26.04 on amd64\n",
		},
		{
			name: "conditions on facts",
			text: `{{if eq .Facts.OS "ubuntu"}}apt{{else}}dnf{{end}}`,
			want: "apt",
		},
		{
			name: "a missing var",
			text: "{{.Vars.emial}}",
			err:  `template: files/gitconfig.tmpl:1:7: executing "files/gitconfig.tmpl" at <.Vars.emial>: map has no entry for key "emial"`,
		},
		{
			name: "no env function",
			text: `{{env "MAISYS_TEST_SECRET"}}`,
			err:  `template: files/gitconfig.tmpl:1: function "env" not defined`,
		},
		{
			name: "no environment in the data",
			text: `{{.Env.MAISYS_TEST_SECRET}}`,
			err:  `can't evaluate field Env`,
		},
		{
			name: "no environment in the vars",
			text: `{{.Vars.MAISYS_TEST_SECRET}}`,
			err:  `map has no entry for key "MAISYS_TEST_SECRET"`,
		},
		{
			name: "a syntax error",
			text: "{{.Vars.email",
			err:  "template: files/gitconfig.tmpl:1: unclosed action",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := file.Template("files/gitconfig.tmpl", tt.text, vars)(e.rt)
			switch {
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Errorf("rendering = %q, %v; want an error containing %q", got, err, tt.err)
			case tt.err == "" && (err != nil || string(got) != tt.want):
				t.Errorf("rendering = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	// Without vars, .Vars is empty rather than missing.
	if _, err := file.Template("t", "{{.Vars.x}}", nil)(e.rt); err == nil || !strings.Contains(err.Error(), `no entry for key "x"`) {
		t.Errorf("rendering without vars: error = %v, want a missing key", err)
	}
}

// repo is a config repo with files to copy.
var repo = fstest.MapFS{
	"files/gitconfig.tmpl":        {Data: []byte("[user]\n\temail = {{.Vars.email}}\n")},
	"files/ssh_config":            {Data: []byte("Host *\n")},
	"files/nvim/init.lua":         {Data: []byte("require('plugins')\n"), Mode: 0o644},
	"files/nvim/lua/plugins.lua":  {Data: []byte("return {}\n"), Mode: 0o644},
	"files/nvim/after/ftplugin.x": {Data: []byte("x\n"), Mode: 0o644},
	"files/bin/hello":             {Data: []byte("#!/bin/sh\necho hello\n"), Mode: 0o755},
}

func TestSourceFiles(t *testing.T) {
	tests := []struct {
		name string
		src  file.Source
		ids  []string
		exec []bool
	}{
		{
			name: "a file",
			src:  file.Source{Path: "files/ssh_config", Target: "~/.ssh/config"},
			ids:  []string{"file:~/.ssh/config"},
			exec: []bool{false},
		},
		{
			name: "a directory, file by file",
			src:  file.Source{Path: "files/nvim/", Target: "~/.config/nvim/"},
			ids: []string{
				"file:~/.config/nvim/after/ftplugin.x",
				"file:~/.config/nvim/init.lua",
				"file:~/.config/nvim/lua/plugins.lua",
			},
			exec: []bool{false, false, false},
		},
		{
			name: "a directory with a target without a slash",
			src:  file.Source{Path: "files/bin/", Target: "~/bin"},
			ids:  []string{"file:~/bin/hello"},
			exec: []bool{true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.src.FS = repo
			files, err := tt.src.Files()
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			var exec []bool
			for _, f := range files {
				ids = append(ids, f.ID())
				exec = append(exec, f.Executable)
			}
			if strings.Join(ids, " ") != strings.Join(tt.ids, " ") || len(exec) != len(tt.exec) {
				t.Fatalf("Files() = %q, want %q", ids, tt.ids)
			}
			for i := range exec {
				if exec[i] != tt.exec[i] {
					t.Errorf("%s is executable: %t, want %t", ids[i], exec[i], tt.exec[i])
				}
			}
		})
	}

	for _, src := range []file.Source{
		{FS: repo, Path: "files/nvim", Target: "~/.config/nvim"},
		{FS: repo, Path: "files/missing", Target: "~/x"},
		{FS: repo, Path: "files/missing/", Target: "~/x/"},
	} {
		if files, err := src.Files(); err == nil {
			t.Errorf("Files() of %s = %d files, want an error", src.Path, len(files))
		}
	}
}

func TestSourceApply(t *testing.T) {
	e := newEnv(t)
	e.write(".config/nvim/mine.lua", "mine\n", 0o644)
	sources := []file.Source{
		{FS: repo, Path: "files/gitconfig.tmpl", Target: "~/.gitconfig", Template: true, Vars: map[string]string{"email": "phil@example.com"}},
		{FS: repo, Path: "files/nvim/", Target: "~/.config/nvim/"},
		{FS: repo, Path: "files/ssh_config", Target: "~/.ssh/config", Mode: 0o600, CRLF: true},
	}
	for _, src := range sources {
		files, err := src.Files()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if c, _ := e.apply(f); c.Action != resource.Create {
				t.Errorf("Check() of %s = %s %q, want create", f.ID(), c.Action, c.Summary)
			}
		}
	}

	want := map[string]string{
		".gitconfig":                    "[user]\n\temail = phil@example.com\n",
		".config/nvim/init.lua":         "require('plugins')\n",
		".config/nvim/lua/plugins.lua":  "return {}\n",
		".config/nvim/after/ftplugin.x": "x\n",
		".config/nvim/mine.lua":         "mine\n",
		".ssh/config":                   "Host *\r\n",
	}
	for rel, content := range want {
		if got := e.read(rel); got != content {
			t.Errorf("~/%s holds %q, want %q", rel, got, content)
		}
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in   string
		want uint32
		err  bool
	}{
		{"", 0, false},
		{"0600", 0o600, false},
		{"755", 0o755, false},
		{"0777", 0o777, false},
		{"1777", 0, true},
		{"0800", 0, true},
		{"rw-r--r--", 0, true},
	}
	for _, tt := range tests {
		got, err := file.ParseMode(tt.in)
		if uint32(got) != tt.want || (err != nil) != tt.err {
			t.Errorf("ParseMode(%q) = %04o, %v; want %04o, error %t", tt.in, got, err, tt.want, tt.err)
		}
	}
}
