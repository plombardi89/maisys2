package version

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func TestProxyList(t *testing.T) {
	tests := []struct {
		goproxy string
		want    []proxy
	}{
		{"https://proxy.golang.org,direct", []proxy{{url: "https://proxy.golang.org"}, {url: "direct"}}},
		{"https://a.example|https://b.example,off", []proxy{{url: "https://a.example", onAnyError: true}, {url: "https://b.example"}, {url: "off"}}},
		{"goproxy.example.com/sub, direct", []proxy{{url: "https://goproxy.example.com/sub"}, {url: "direct"}}},
		{",,http://insecure.example|", []proxy{{url: "http://insecure.example", onAnyError: true}}},
		{"off", []proxy{{url: "off"}}},
	}
	for _, tt := range tests {
		if got := proxyList(tt.goproxy); !slices.Equal(got, tt.want) {
			t.Errorf("proxyList(%q) = %+v, want %+v", tt.goproxy, got, tt.want)
		}
	}
}

// env returns a Getenv reading vars, and nothing else: no go env file.
func env(vars map[string]string) func(string) string {
	return func(key string) string {
		if key == "GOENV" && vars[key] == "" {
			return "off"
		}
		return vars[key]
	}
}

// direct records the lookups a GoModule makes without a proxy.
type direct struct {
	paths []string
}

func (d *direct) lookup(_ context.Context, path string) (string, error) {
	d.paths = append(d.paths, path)
	return "v9.9.9", nil
}

func TestGoModule(t *testing.T) {
	a := newServer(t)
	a.file("/example.com/mod/@latest", `{"Version": "v1.2.3", "Time": "2026-10-01T20:21:39Z"}`)
	a.file("/github.com/!burnt!sushi/toml/@latest", `{"Version": "v1.5.0"}`)
	a.handle("/fails.example/mod/@latest", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "broken", http.StatusInternalServerError)
	})
	a.file("/odd.example/mod/@latest", `{"Version": "latest"}`)
	b := newServer(t)
	b.file("/only-b.example/mod/@latest", `{"Version": "v2.0.0"}`)
	b.file("/fails.example/mod/@latest", `{"Version": "v3.0.0"}`)

	tests := []struct {
		name       string
		path       string
		env        map[string]string
		want       string
		wantErr    string
		wantA      []string // requests to a
		wantB      []string // requests to b
		wantDirect []string
	}{
		{
			name:  "a package in a module",
			path:  "example.com/mod/cmd/tool",
			env:   map[string]string{"GOPROXY": a.URL},
			want:  "v1.2.3",
			wantA: []string{"/example.com/mod/cmd/tool/@latest", "/example.com/mod/cmd/@latest", "/example.com/mod/@latest"},
		},
		{
			name:  "upper case",
			path:  "github.com/BurntSushi/toml",
			env:   map[string]string{"GOPROXY": a.URL},
			want:  "v1.5.0",
			wantA: []string{"/github.com/!burnt!sushi/toml/@latest"},
		},
		{
			name:    "no such module",
			path:    "nowhere.example/mod",
			env:     map[string]string{"GOPROXY": a.URL},
			wantErr: a.URL + " has no module that provides nowhere.example/mod",
			wantA:   []string{"/nowhere.example/mod/@latest", "/nowhere.example/@latest"},
		},
		{
			name:  "after a comma, the next proxy if the first hasn't the module",
			path:  "only-b.example/mod",
			env:   map[string]string{"GOPROXY": a.URL + "," + b.URL},
			want:  "v2.0.0",
			wantA: []string{"/only-b.example/mod/@latest", "/only-b.example/@latest"},
			wantB: []string{"/only-b.example/mod/@latest"},
		},
		{
			name:    "after a comma, not if the first fails",
			path:    "fails.example/mod",
			env:     map[string]string{"GOPROXY": a.URL + "," + b.URL},
			wantErr: "500 Internal Server Error",
			wantA:   []string{"/fails.example/mod/@latest"},
		},
		{
			name:  "after a pipe, also if the first fails",
			path:  "fails.example/mod",
			env:   map[string]string{"GOPROXY": a.URL + "|" + b.URL},
			want:  "v3.0.0",
			wantA: []string{"/fails.example/mod/@latest"},
			wantB: []string{"/fails.example/mod/@latest"},
		},
		{
			name:       "direct after the proxy",
			path:       "only-b.example/mod",
			env:        map[string]string{"GOPROXY": a.URL + ",direct"},
			want:       "v9.9.9",
			wantA:      []string{"/only-b.example/mod/@latest", "/only-b.example/@latest"},
			wantDirect: []string{"only-b.example/mod"},
		},
		{
			name:    "off",
			path:    "example.com/mod",
			env:     map[string]string{"GOPROXY": "off"},
			wantErr: "GOPROXY is off",
		},
		{
			name:    "off after the proxy",
			path:    "only-b.example/mod",
			env:     map[string]string{"GOPROXY": a.URL + ",off"},
			wantErr: "has no module that provides only-b.example/mod",
			wantA:   []string{"/only-b.example/mod/@latest", "/only-b.example/@latest"},
		},
		{
			name:       "GOPRIVATE",
			path:       "example.com/mod/cmd/tool",
			env:        map[string]string{"GOPROXY": a.URL, "GOPRIVATE": "example.com/*,corp.example"},
			want:       "v9.9.9",
			wantDirect: []string{"example.com/mod/cmd/tool"},
		},
		{
			name:  "GONOPROXY overrides GOPRIVATE",
			path:  "example.com/mod",
			env:   map[string]string{"GOPROXY": a.URL, "GOPRIVATE": "example.com", "GONOPROXY": "none.example"},
			want:  "v1.2.3",
			wantA: []string{"/example.com/mod/@latest"},
		},
		{
			name:    "a version that isn't one",
			path:    "odd.example/mod",
			env:     map[string]string{"GOPROXY": a.URL},
			wantErr: `"latest" isn't a version`,
			wantA:   []string{"/odd.example/mod/@latest"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startA, startB := len(a.got()), len(b.got())
			d := &direct{}
			g := GoModule{Path: tt.path, Getenv: env(tt.env), Direct: d.lookup}
			latest(t, a, g, tt.want, tt.wantErr)
			if got := a.got()[startA:]; !slices.Equal(got, tt.wantA) {
				t.Errorf("requests to the first proxy = %q, want %q", got, tt.wantA)
			}
			if got := b.got()[startB:]; !slices.Equal(got, tt.wantB) {
				t.Errorf("requests to the second proxy = %q, want %q", got, tt.wantB)
			}
			if !slices.Equal(d.paths, tt.wantDirect) {
				t.Errorf("direct lookups = %q, want %q", d.paths, tt.wantDirect)
			}
		})
	}
}

func TestGoModuleWithoutDirect(t *testing.T) {
	tests := []struct {
		env     map[string]string
		wantErr string
	}{
		{map[string]string{"GOPROXY": "direct"}, "maisys can only look corp.example/tool up through a module proxy, and GOPROXY says to fetch it directly"},
		{map[string]string{"GOPRIVATE": "corp.example"}, "maisys can only look corp.example/tool up through a module proxy, and it matches GONOPROXY or GOPRIVATE"},
	}
	for _, tt := range tests {
		_, err := GoModule{Path: "corp.example/tool", Getenv: env(tt.env)}.Latest(t.Context(), &download.Client{})
		if err == nil || err.Error() != tt.wantErr {
			t.Errorf("Latest() with %v: error = %v, want %q", tt.env, err, tt.wantErr)
		}
	}
}

func TestGoModuleEnvFile(t *testing.T) {
	s := newServer(t)
	s.file("/example.com/mod/@latest", `{"Version": "v1.2.3"}`)
	home := t.TempDir()
	envFile := filepath.Join(home, ".config", "go", "env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("GOPROXY="+s.URL+"\nGOPRIVATE=corp.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"HOME": home, "AppData": filepath.Join(home, ".config")}
	getenv := func(key string) string { return vars[key] }

	latest(t, s, GoModule{Path: "example.com/mod", Getenv: getenv}, "v1.2.3", "")
	d := &direct{}
	latest(t, s, GoModule{Path: "corp.example/tool", Getenv: getenv, Direct: d.lookup}, "v9.9.9", "")
	if !slices.Equal(d.paths, []string{"corp.example/tool"}) {
		t.Errorf("GOPRIVATE from the go env file: direct lookups = %q", d.paths)
	}

	// The environment wins over the file.
	vars["GOPROXY"] = "off"
	latest(t, s, GoModule{Path: "example.com/mod", Getenv: getenv}, "", "GOPROXY is off")
}

func TestGoModuleLookupError(t *testing.T) {
	s := newServer(t)
	s.handle("/fails.example/mod/@latest", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	goList := func(_ context.Context, path string) (string, error) {
		return "", errors.New("go list -m " + path + "@latest: go isn't installed")
	}
	tests := []struct {
		name    string
		path    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "GOPRIVATE",
			path:    "corp.example/tool",
			env:     map[string]string{"GOPROXY": s.URL, "GOPRIVATE": "corp.example"},
			wantErr: "can't find the latest version of corp.example/tool from go list -m (direct): go list -m corp.example/tool@latest: go isn't installed (pin a version to stop depending on it)",
		},
		{
			name:    "GOPROXY=direct",
			path:    "corp.example/tool",
			env:     map[string]string{"GOPROXY": "direct"},
			wantErr: "can't find the latest version of corp.example/tool from go list -m (direct): go list -m corp.example/tool@latest: go isn't installed (pin a version to stop depending on it)",
		},
		{
			name:    "not in the proxy",
			path:    "corp.example/tool",
			env:     map[string]string{"GOPROXY": s.URL + ",direct"},
			wantErr: "can't find the latest version of corp.example/tool from go list -m (direct): go list -m corp.example/tool@latest: go isn't installed (pin a version to stop depending on it)",
		},
		{
			name:    "the proxy fails",
			path:    "fails.example/mod",
			env:     map[string]string{"GOPROXY": s.URL + ",direct"},
			wantErr: "can't find the latest version of fails.example/mod from the Go module proxy (fails.example/mod): " + s.URL + "/fails.example/mod/@latest: 503 Service Unavailable (down) (pin a version to stop depending on it)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := GoModule{Path: tt.path, Getenv: env(tt.env), Direct: goList}
			for _, c := range []*Cache{{Path: filepath.Join(t.TempDir(), "latest.json")}, nil} {
				_, err := c.Resolve(t.Context(), newClient(s), tt.path, g)
				if _, ok := errors.AsType[*LookupError](err); !ok || err.Error() != tt.wantErr {
					t.Errorf("Resolve() error = %v, want a *LookupError %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestGoModuleHidesPasswords(t *testing.T) {
	s := newServer(t)
	proxyURL := strings.Replace(s.URL, "://", "://me:secret@", 1)
	_, err := GoModule{Path: "example.com/mod", Getenv: env(map[string]string{"GOPROXY": proxyURL})}.Latest(t.Context(), newClient(s))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("Latest() error = %v, want one without the password", err)
	}
}

func TestGoList(t *testing.T) {
	r := systemtest.NewFakeRunner(t)
	r.On("/usr/bin/go list -m -json corp.example/mod/cmd/tool@latest", systemtest.Fail(1, "go: module corp.example/mod/cmd/tool: no matching versions"))
	r.On("/usr/bin/go list -m -json corp.example/mod/cmd@latest", systemtest.Fail(1, "go: module corp.example/mod/cmd: not found"))
	r.On("/usr/bin/go list -m -json corp.example/mod@latest", systemtest.Respond(0, `{"Path": "corp.example/mod", "Version": "v0.4.0"}`))
	lookup := GoList(r, "/usr/bin/go")

	got, err := lookup(t.Context(), "corp.example/mod/cmd/tool")
	if err != nil || got != "v0.4.0" {
		t.Fatalf("lookup() = %q, %v, want v0.4.0", got, err)
	}
	want := []string{
		"/usr/bin/go list -m -json corp.example/mod/cmd/tool@latest",
		"/usr/bin/go list -m -json corp.example/mod/cmd@latest",
		"/usr/bin/go list -m -json corp.example/mod@latest",
	}
	if got := r.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	for _, c := range r.Calls() {
		if c.Cmd.Root || !slices.Contains(c.Cmd.Env, "GOWORK=off") {
			t.Errorf("%s ran as root or inside a workspace", c.Cmd)
		}
	}

	r.On("/usr/bin/go list -m -json corp.example@latest", systemtest.Fail(1, "go: corp.example: not found"))
	r.On("/usr/bin/go list -m -json corp.example/gone@latest", systemtest.Fail(1, "go: module corp.example/gone: not found"))
	_, err = lookup(t.Context(), "corp.example/gone")
	if ee, ok := errors.AsType[*system.ExitError](err); !ok || !strings.Contains(ee.Error(), "corp.example/gone@latest") {
		t.Errorf("lookup() of a missing module: error = %v, want the first go list's", err)
	}

	missing := systemtest.NewFakeRunner(t)
	missing.On("go", func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: -1}, errors.New("go isn't installed")
	})
	if _, err := GoList(missing, "go")(t.Context(), "corp.example/mod"); err == nil || !strings.Contains(err.Error(), "go isn't installed") {
		t.Errorf("lookup() without go: error = %v", err)
	}
	if n := len(missing.Commands()); n != 1 {
		t.Errorf("lookup() without go ran %d commands, want 1", n)
	}
}

func TestGoListRunsInAModuleOfItsOwn(t *testing.T) {
	r := systemtest.NewFakeRunner(t)
	var dirs []string
	r.On("go list -m -json", func(_ context.Context, c system.Cmd) (system.Result, error) {
		dirs = append(dirs, c.Dir)
		if data, err := os.ReadFile(filepath.Join(c.Dir, "go.mod")); err != nil || string(data) != "module maisys.invalid/lookup\n\ngo 1.21\n" {
			t.Errorf("go list ran in %s, whose go.mod holds %q (%v), want maisys's own", c.Dir, data, err)
		}
		for _, kv := range []string{"GO111MODULE=on", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly"} {
			if !slices.Contains(c.Env, kv) {
				t.Errorf("go list ran with environment %q, want %s", c.Env, kv)
			}
		}
		if c.Args[3] == "corp.example/mod@latest" {
			return system.Result{Stdout: []byte(`{"Version": "v0.4.0"}`)}, nil
		}
		return system.Result{ExitCode: 1}, nil
	})
	lookup := GoList(r, "go")
	for range 2 {
		if got, err := lookup(t.Context(), "corp.example/mod/cmd/tool"); err != nil || got != "v0.4.0" {
			t.Fatalf("lookup() = %q, %v, want v0.4.0", got, err)
		}
	}

	if len(dirs) != 6 || dirs[0] != dirs[2] || dirs[3] == dirs[0] || dirs[3] != dirs[5] {
		t.Errorf("go list ran in %q, want one folder for each lookup", dirs)
	}
	for _, dir := range dirs {
		if filepath.Clean(dir) == filepath.Clean(os.TempDir()) {
			t.Errorf("go list ran in %s, the temporary folder, where any go.mod above it applies", dir)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after the lookup", dir)
		}
	}
}

// TestGoListWithGo runs the real go list against a module proxy in a
// folder, with settings that would each change its answer if go list
// followed them: a go.mod above the temporary folder, GOFLAGS, from the
// environment or the go env file, and GOTOOLCHAIN.
func TestGoListWithGo(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go isn't installed")
	}
	root := t.TempDir()
	proxy := filepath.Join(root, "proxy")
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		writeFile(t, filepath.Join(proxy, "example.com", "mod", "@v", v+".info"), `{"Version": "`+v+`", "Time": "2026-10-01T00:00:00Z"}`)
		writeFile(t, filepath.Join(proxy, "example.com", "mod", "@v", v+".mod"), "module example.com/mod\n\ngo 1.21\n")
	}
	writeFile(t, filepath.Join(proxy, "example.com", "mod", "@v", "list"), "v1.0.0\nv1.1.0\n")
	excludes := filepath.Join(root, "excludes.mod")
	writeFile(t, excludes, "module stray\n\ngo 1.21\n\nexclude example.com/mod v1.1.0\n")
	goEnv := filepath.Join(root, "goenv")
	writeFile(t, goEnv, "GOFLAGS=-mod=vendor\n")

	tests := []struct {
		name  string
		above string // the go.mod above the temporary folder
		env   map[string]string
		path  string
	}{
		{name: "a go.mod above asking for a newer go", above: "module stray\n\ngo 1.999\n"},
		{name: "a go.mod above excluding the latest version", above: "module stray\n\ngo 1.21\n\nexclude example.com/mod v1.1.0\n"},
		{name: "GOFLAGS=-mod=vendor", env: map[string]string{"GOFLAGS": "-mod=vendor"}},
		{name: "GOFLAGS=-modfile", env: map[string]string{"GOFLAGS": "-modfile=" + excludes}},
		{name: "GOFLAGS=-e", env: map[string]string{"GOFLAGS": "-e"}, path: "example.com/mod/cmd/tool"},
		{name: "GOFLAGS in the go env file", env: map[string]string{"GOENV": goEnv}},
		{name: "GOTOOLCHAIN", env: map[string]string{"GOTOOLCHAIN": "go1.999.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "above")
			tmp := filepath.Join(dir, "tmp")
			if err := os.MkdirAll(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.above != "" {
				writeFile(t, filepath.Join(dir, "go.mod"), tt.above)
			}
			env := map[string]string{
				"TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
				"GOENV":       "off",
				"GOFLAGS":     "",
				"GOTOOLCHAIN": "",
				"GOPROXY":     "file:///" + strings.TrimPrefix(filepath.ToSlash(proxy), "/"),
				"GOPRIVATE":   "",
				"GONOPROXY":   "",
				"GOSUMDB":     "off",
				"GOMODCACHE":  filepath.Join(root, "modcache"),
				"GOPATH":      filepath.Join(root, "gopath"),
				"GOCACHE":     filepath.Join(root, "gocache"),
			}
			maps.Copy(env, tt.env)
			for key, value := range env {
				t.Setenv(key, value)
			}

			got, err := GoList(system.NewRunner(system.Options{}), goBin)(t.Context(), cmp.Or(tt.path, "example.com/mod"))
			if err != nil || got != "v1.1.0" {
				t.Errorf("lookup() = %q, %v, want v1.1.0", got, err)
			}
			if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
				t.Errorf("the temporary folder holds %v after the lookup, want nothing", entries)
			}
		})
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
