package cli

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
)

// latestUpstream is where a tool's latest version comes from.
type latestUpstream struct {
	mu      sync.Mutex
	version string
	lookups int
}

func (u *latestUpstream) Upstream() string { return "https://example.invalid/tool/releases/latest" }

func (u *latestUpstream) Latest(context.Context, *download.Client) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.lookups++
	return u.version, nil
}

// set releases version v.
func (u *latestUpstream) set(v string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.version = v
}

// count returns how many times the latest version was looked up.
func (u *latestUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lookups
}

// latestModule is a module with a tool on "latest", which looks the
// version up through the sync's tool environment, as the tool modules do.
// Every plan installs it again, at the version it found.
type latestModule struct {
	upstream  *latestUpstream
	installed []string
}

func (m *latestModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: "latest", Summary: "A test module.", OS: config.AllOS()}
}

func (m *latestModule) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (m *latestModule) DependsOn() []string { return nil }

func (m *latestModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	return []resource.Resource{&latestTool{m: m}}, nil
}

type latestTool struct {
	m       *latestModule
	version string
}

func (r *latestTool) ID() string        { return "fake:tool" }
func (r *latestTool) Kind() string      { return "fake" }
func (r *latestTool) Targets() []string { return nil }
func (r *latestTool) Privileged() bool  { return false }

func (r *latestTool) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	env, err := tool.EnvFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	if r.version, err = env.Latest.Resolve(ctx, env.Client, "tool", r.m.upstream); err != nil {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Create, Summary: "tool " + r.version + " (latest)"}, nil
}

func (r *latestTool) Apply(context.Context, *resource.Runtime) (state.Record, error) {
	r.m.installed = append(r.m.installed, r.version)
	return state.Record{}, nil
}

// TestSyncAfterDryRun checks that a sync installs the latest versions the
// dry run before it showed, and the next sync looks them up again.
func TestSyncAfterDryRun(t *testing.T) {
	e := newTestEnv(t)
	u := &latestUpstream{version: "1.0.0"}
	m := &latestModule{upstream: u}
	e.modules = []config.Module{m}
	dir := writeTemp(t, map[string]string{"maisys.toml": "config_version = 1\n", "machines/box.toml": "os = \"fedora\"\n\n[latest]\nenabled = true\n"})
	e.saveSettings(settings.Settings{RepoDir: dir, Machine: "box"})

	steps := []struct {
		args     []string
		code     int
		shows    string
		lookups  int
		installs []string
	}{
		{args: []string{"--dry-run"}, code: exitChanges, shows: "+ tool 1.0.0 (latest)", lookups: 1},
		{args: []string{"--yes"}, code: exitOK, shows: "+ tool 1.0.0 (latest)", lookups: 1, installs: []string{"1.0.0"}},
		{args: []string{"--yes"}, code: exitOK, shows: "+ tool 1.1.0 (latest)", lookups: 2, installs: []string{"1.0.0", "1.1.0"}},
	}
	for i, step := range steps {
		if i == 1 {
			u.set("1.1.0")
		}
		args := append([]string{"sync", "--local"}, step.args...)
		code, stdout, stderr := e.run(args...)
		if code != step.code || !strings.Contains(stdout, step.shows) {
			t.Fatalf("%s: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d and %q", strings.Join(args, " "), code, stdout, stderr, step.code, step.shows)
		}
		if n := u.count(); n != step.lookups {
			t.Errorf("%s: looked the latest version up %d times in all, want %d", strings.Join(args, " "), n, step.lookups)
		}
		if strings.Join(m.installed, " ") != strings.Join(step.installs, " ") {
			t.Errorf("%s: installed %q in all, want %q", strings.Join(args, " "), m.installed, step.installs)
		}
	}
}
