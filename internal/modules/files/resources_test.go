package files_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/files"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
)

var repo = fstest.MapFS{
	"files/gitconfig.tmpl":        {Data: []byte("[user]\n\temail = {{ .Vars.email }}\n# {{ .Machine.Name }} on {{ .Facts.OS }}\n")},
	"files/ssh_config":            {Data: []byte("Host *\n  AddKeysToAgent yes\n")},
	"files/nvim/init.lua":         {Data: []byte("require('plugins')\n")},
	"files/nvim/lua/plugins.lua":  {Data: []byte("return {}\n")},
	"files/bin/hello":             {Data: []byte("#!/bin/sh\necho hello\n"), Mode: 0o755},
	"files/windows/settings.json": {Data: []byte("{\n}\n")},
}

func TestResources(t *testing.T) {
	cfg := &files.Config{
		Enabled: true,
		Vars:    map[string]string{"email": "phil@example.com"},
		Items: map[string]files.Item{
			"gitconfig":  {Enabled: true, Source: "files/gitconfig.tmpl", Target: "~/.gitconfig", Template: true},
			"ssh_config": {Enabled: true, Source: "files/ssh_config", Target: "~/.ssh/config", Mode: "0600"},
			"nvim":       {Enabled: true, Source: "files/nvim/", Target: "~/.config/nvim/"},
			"hello":      {Enabled: true, Source: "files/bin/hello", Target: "~/.local/bin/hello"},
			"settings":   {Enabled: true, Source: "files/windows/settings.json", Target: "~/settings.json", EOL: "crlf"},
			"off":        {Enabled: false, Source: "files/missing", Target: "~/missing"},
		},
	}
	home := t.TempDir()
	rt := &resource.Runtime{
		Paths:   settings.Paths{Home: home},
		Machine: &config.Machine{Name: "box", OS: config.Fedora},
		Facts:   &facts.System{OS: config.Fedora},
		RepoFS:  repo,
	}
	rs, err := files.Module{}.Resources(cfg, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	want := []string{
		"file:~/.gitconfig",
		"file:~/.local/bin/hello",
		"file:~/.config/nvim/init.lua",
		"file:~/.config/nvim/lua/plugins.lua",
		"file:~/settings.json",
		"file:~/.ssh/config",
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("resources =\n%q\nwant\n%q", ids, want)
	}

	for _, r := range rs {
		c, err := r.Check(t.Context(), rt)
		if err != nil || c.Action != resource.Create {
			t.Errorf("%s: Check() = %+v, %v, want it created", r.ID(), c, err)
		}
		if _, err := r.Apply(t.Context(), rt); err != nil {
			t.Fatalf("%s: Apply() error = %v", r.ID(), err)
		}
	}
	contents := map[string]string{
		".gitconfig":                   "[user]\n\temail = phil@example.com\n# box on fedora\n",
		".config/nvim/lua/plugins.lua": "return {}\n",
		"settings.json":                "{\r\n}\r\n",
	}
	for rel, want := range contents {
		got, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("~/%s = %q, %v, want %q", rel, got, err, want)
		}
	}
	if runtime.GOOS != "windows" {
		modes := map[string]os.FileMode{".ssh/config": 0o600, ".local/bin/hello": 0o755, ".gitconfig": 0o644}
		for rel, want := range modes {
			info, err := os.Stat(filepath.Join(home, filepath.FromSlash(rel)))
			if err != nil || info.Mode().Perm() != want {
				t.Errorf("~/%s: mode %v, %v, want %v", rel, info.Mode().Perm(), err, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, "missing")); err == nil {
		t.Error("the item turned off was copied")
	}
}

func TestResourcesErrors(t *testing.T) {
	tests := []struct {
		name string
		item files.Item
		fs   bool
		want string
	}{
		{
			name: "a missing source",
			item: files.Item{Enabled: true, Source: "files/missing", Target: "~/missing"},
			fs:   true,
			want: "item one: reading files/missing in the config repo: open files/missing: file does not exist",
		},
		{
			name: "a bad mode",
			item: files.Item{Enabled: true, Source: "files/ssh_config", Target: "~/.ssh/config", Mode: "rw"},
			fs:   true,
			want: `item one: the mode must be octal permission bits such as "0600", not "rw"`,
		},
		{
			name: "no config repo",
			item: files.Item{Enabled: true, Source: "files/ssh_config", Target: "~/.ssh/config"},
			want: "the config repo's files aren't available",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{Paths: settings.Paths{Home: t.TempDir()}}
			if tt.fs {
				rt.RepoFS = repo
			}
			cfg := &files.Config{Enabled: true, Items: map[string]files.Item{"one": tt.item}}
			if _, err := (files.Module{}).Resources(cfg, rt); err == nil || err.Error() != tt.want {
				t.Errorf("Resources() error = %v, want %q", err, tt.want)
			}
		})
	}
}
