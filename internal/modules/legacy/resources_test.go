package legacy

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
)

// readme is protoc's readme.txt, as its release zips have it.
const readme = `Protocol Buffers - Google's data interchange format
Copyright 2008 Google Inc.
https://developers.google.com/protocol-buffers/
This package contains a precompiled binary version of the protocol buffer
compiler (protoc). This binary is intended for users who want to use Protocol
Buffers in languages other than C++ but do not want to compile protoc
themselves. To install, simply place this binary somewhere in your PATH.
If you intend to use the included well known types then don't forget to
copy the contents of the 'include' directory somewhere as well, for example
into '/usr/local/include/'.
Please refer to our official github site for more installation instructions:
  https://github.com/protocolbuffers/protobuf
`

func TestResources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the legacy module only runs on Linux")
	}
	tests := []struct {
		name   string
		files  map[string]string
		goRoot bool
		want   []string
	}{
		{
			name:   "everything, and go install's mage and golangci-lint, which are left alone",
			goRoot: true,
			files: map[string]string{
				"bin/kubectl":                       "",
				"bin/stern":                         "",
				"bin/yq":                            "",
				"bin/lnav":                          "",
				"bin/protoc":                        "",
				"bin/mutagen/mutagen":               "",
				"go/bin/mage":                       "",
				"go/bin/golangci-lint":              "",
				"include/google/protobuf/any.proto": "",
				"readme.txt":                        readme,
				"Apps/idea/bin/idea":                "",
				"Apps/idea.bak/bin/idea":            "",
				"venv/maisys/bin/python":            "",
			},
			want: []string{
				"GOROOT", "~/bin/kubectl", "~/bin/stern", "~/bin/yq", "~/bin/lnav", "~/bin/protoc", "~/bin/mutagen",
				"~/include/google", "~/readme.txt", "~/Apps/idea", "~/Apps/idea.bak", "~/venv/maisys",
			},
		},
		{
			name: "some, and a readme that isn't protoc's",
			files: map[string]string{
				"bin/yq":          "",
				"bin/other":       "",
				"go/bin/gopls":    "",
				"readme.txt":      "my notes\n",
				"include/other.h": "",
			},
			want: []string{"~/bin/yq"},
		},
		{
			name: "nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			old := goRoot
			goRoot = filepath.Join(t.TempDir(), "go")
			t.Cleanup(func() { goRoot = old })
			if tt.goRoot {
				write(t, goRoot, "bin/go", "")
			}
			for rel, data := range tt.files {
				write(t, home, rel, data)
			}

			rt := &resource.Runtime{Paths: settings.Paths{Home: home}}
			rs, err := Module{}.Resources(&Config{Enabled: true}, rt)
			if err != nil {
				t.Fatal(err)
			}
			if len(rs) != 12 {
				t.Errorf("%d resources, want one for each of the 12 leftovers", len(rs))
			}
			var removed []string
			for _, r := range rs {
				c, err := r.Check(t.Context(), rt)
				if err != nil {
					t.Fatalf("%s: %v", r.ID(), err)
				}
				switch c.Action {
				case resource.Remove:
					removed = append(removed, strings.Replace(c.Summary, goRoot, "GOROOT", 1))
					if r.Privileged() != (c.Summary == goRoot) {
						t.Errorf("%s: Privileged() = %v, want only Go's folder deleted as root", r.ID(), r.Privileged())
					}
				case resource.NoChange:
				default:
					t.Errorf("%s: Check() = %+v, want it removed or left alone", r.ID(), c)
				}
			}
			if !slices.Equal(removed, tt.want) {
				t.Errorf("removed =\n%q\nwant\n%q", removed, tt.want)
			}
		})
	}
}

func TestDependsOn(t *testing.T) {
	want := []string{"shell", "golang", "kubectl", "stern", "yq", "lnav", "protoc", "mutagen", "python_tools", "intellij"}
	if deps := (Module{}).DependsOn(); !slices.Equal(deps, want) {
		t.Errorf("DependsOn() = %q, want %q: the modules that replace what the cleanup deletes, and no others", deps, want)
	}
}

func write(t *testing.T, dir, rel, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
