// Package uvtool keeps Python tools installed with uv tool install, such as
// the python_tools module's, at their latest version.
//
// The tools of one uv are listed once per sync for every Tool made from it:
// uv tool list says what's installed, and uv tool list --outdated says
// which tools have a newer version in the package index, following uv's
// own settings. A tool that's missing is installed, and one with a newer
// version is upgraded with uv tool upgrade, so a sync that changes nothing
// runs neither. uv only has --outdated since 0.10.10; with an older uv,
// tools are installed but never upgraded.
//
// A tool taken out of the config is removed with uv tool uninstall. If uv
// itself is gone, as when the same sync removes it, the tool's environment
// and executables are deleted as the state file recorded them.
package uvtool

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Tool is.
const Kind = "uv-tool"

// UV is a uv binary that installs tools. It lists its tools the first
// time a Tool made from it is checked, and remembers the list, so make a
// new UV for each sync. It's safe for concurrent use.
type UV struct {
	// Path is the uv binary. Empty means uv from PATH. If the binary
	// doesn't exist yet, as when the same sync installs uv first, tools
	// can't be listed and are planned to be installed.
	Path string
	// Env is extra environment for uv, as NAME=value.
	Env []string

	mu     sync.Mutex
	listed bool
	tools  map[string]listed
	err    error
}

// Tool returns the tool name, installed with u.
func (u *UV) Tool(name string) *Tool {
	return &Tool{Name: name, UV: u}
}

// Tool is a Python tool installed with uv tool install. Its ID is
// "uv-tool:" followed by its normalized name.
type Tool struct {
	// Name is the name of the tool's package in the index, such as
	// "azure-cli".
	Name string
	// UV is the uv that installs it.
	UV *UV

	// upgrade reports whether Check found the tool installed, but out of
	// date.
	upgrade bool
}

var _ resource.Resource = (*Tool)(nil)

// ID returns "uv-tool:" followed by the tool's normalized name.
func (t *Tool) ID() string { return Kind + ":" + normalize(t.Name) }

// Kind returns "uv-tool".
func (t *Tool) Kind() string { return Kind }

// Targets returns nothing: where uv puts tools depends on its settings.
func (t *Tool) Targets() []string { return nil }

// Privileged returns false: uv tools are the user's.
func (t *Tool) Privileged() bool { return false }

// Check looks the tool up in uv's list of installed tools.
func (t *Tool) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if !validName(t.Name) {
		return resource.Change{}, fmt.Errorf("%q isn't the name of a Python package", t.Name)
	}
	t.upgrade = false
	if !t.UV.exists() {
		return resource.Change{Action: resource.Create, Summary: "install " + t.Name}, nil
	}
	tools, err := t.UV.list(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	installed, ok := tools[normalize(t.Name)]
	switch {
	case !ok:
		return resource.Change{Action: resource.Create, Summary: "install " + t.Name}, nil
	case installed.latest != "":
		t.upgrade = true
		return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("upgrade %s %s → %s", t.Name, installed.version, installed.latest)}, nil
	}
	return resource.Change{}, nil
}

// Apply installs the tool, or upgrades it if Check found it out of date.
func (t *Tool) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if !validName(t.Name) {
		return state.Record{}, fmt.Errorf("%q isn't the name of a Python package", t.Name)
	}
	verb := "install"
	if t.upgrade {
		verb = "upgrade"
	}
	if _, err := t.UV.run(ctx, rt, nil, "tool", verb, t.Name); err != nil {
		return state.Record{}, err
	}

	rec := state.Record{Details: map[string]string{"name": t.Name}}
	if t.UV.Path != "" {
		rec.Details["uv"] = t.UV.Path
	}
	// The record says where the tool is, so it can be removed even without
	// uv. Not knowing that only matters then, so it isn't an error.
	res, err := t.UV.run(ctx, rt, nil, "tool", "list", "--show-paths", "--color", "never")
	if err != nil {
		if rt.Log != nil {
			_, _ = fmt.Fprintf(rt.Log, "uv tool %s: couldn't list where it is: %v\n", t.Name, err)
		}
		return rec, nil
	}
	for _, l := range parseList(string(res.Stdout)) {
		if l.name == normalize(t.Name) {
			rec.Details["version"] = l.version
			if l.dir != "" {
				rec.Details["dir"] = l.dir
			}
			rec.Paths = l.executables
		}
	}
	return rec, nil
}

// exists reports whether the uv binary exists. uv from PATH is assumed to.
func (u *UV) exists() bool {
	if u.Path == "" {
		return true
	}
	_, err := os.Stat(u.Path)
	return err == nil
}

// run runs uv with args. allow lists the exit codes other than 0 that
// aren't errors.
func (u *UV) run(ctx context.Context, rt *resource.Runtime, allow []int, args ...string) (system.Result, error) {
	return runUV(ctx, rt.Runner, cmp.Or(u.Path, "uv"), u.Env, allow, args...)
}

func runUV(ctx context.Context, r system.Runner, uv string, env []string, allow []int, args ...string) (system.Result, error) {
	label := "uv " + strings.Join(args[:min(len(args), 3)], " ")
	if i := strings.Index(label, " --"); i >= 0 {
		label = label[:i]
	}
	return r.Run(ctx, system.Cmd{Name: uv, Args: args, Env: env, Label: label, AllowExit: allow})
}

// list returns the installed tools by normalized name, with the newest
// version of those that are out of date. It lists them only the first
// time.
func (u *UV) list(ctx context.Context, rt *resource.Runtime) (map[string]listed, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.listed {
		u.tools, u.err = u.listAll(ctx, rt)
		u.listed = true
	}
	return u.tools, u.err
}

func (u *UV) listAll(ctx context.Context, rt *resource.Runtime) (map[string]listed, error) {
	res, err := u.run(ctx, rt, nil, "tool", "list", "--color", "never")
	if err != nil {
		return nil, fmt.Errorf("couldn't list the installed uv tools: %w", err)
	}
	tools := map[string]listed{}
	for _, l := range parseList(string(res.Stdout)) {
		tools[l.name] = l
	}
	if len(tools) == 0 {
		return tools, nil
	}

	res, err = u.run(ctx, rt, []int{2}, "tool", "list", "--outdated", "--color", "never")
	switch {
	case err != nil:
		return nil, fmt.Errorf("couldn't check the installed uv tools for newer versions: %w", err)
	case res.ExitCode == 2 && strings.Contains(string(res.Stderr), "--outdated"):
		// This uv can't tell which tools are out of date.
		return tools, nil
	case res.ExitCode == 2:
		return nil, fmt.Errorf("couldn't check the installed uv tools for newer versions: %w", system.CheckExit(system.Cmd{Name: "uv", Label: "uv tool list"}, res))
	}
	for _, l := range parseList(string(res.Stdout)) {
		if t, ok := tools[l.name]; ok && l.latest != "" {
			t.latest = l.latest
			tools[l.name] = t
		}
	}
	return tools, nil
}

// listed is a tool in uv tool list's output.
type listed struct {
	// name is the tool's normalized name.
	name string
	// version is its version, without the "v".
	version string
	// latest is its newest version, with --outdated.
	latest string
	// dir is its environment, with --show-paths.
	dir string
	// executables are the paths of its executables, with --show-paths.
	executables []string
}

// latestRE finds the newest version uv tool list --outdated shows.
var latestRE = regexp.MustCompile(`\[latest: ([^\]\s]+)\]`)

// parseList reads uv tool list's output: a line for each tool, such as
// "azure-cli v2.67.0", perhaps followed by " [latest: 2.68.0]" with
// --outdated and " (<environment>)" with --show-paths; then a line for each
// of its executables, such as "- az", followed by " (<path>)" with
// --show-paths.
func parseList(out string) []listed {
	var tools []listed
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if exe, ok := strings.CutPrefix(line, "- "); ok {
			if len(tools) > 0 {
				if p := trailingPath(exe); p != "" {
					t := &tools[len(tools)-1]
					t.executables = append(t.executables, p)
				}
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !validName(fields[0]) || !strings.HasPrefix(fields[1], "v") {
			continue
		}
		t := listed{name: normalize(fields[0]), version: strings.TrimPrefix(fields[1], "v")}
		if m := latestRE.FindStringSubmatch(line); m != nil {
			t.latest = m[1]
		}
		t.dir = trailingPath(line)
		tools = append(tools, t)
	}
	return tools
}

// trailingPath returns the path in brackets at the end of s, as in
// "az (/home/u/.local/bin/az)", or "" if there isn't one.
func trailingPath(s string) string {
	i := strings.LastIndex(s, " (")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return ""
	}
	return s[i+2 : len(s)-1]
}

// nameRE matches the name of a Python package.
var nameRE = regexp.MustCompile(`^(?i:[a-z0-9]|[a-z0-9][a-z0-9._-]*[a-z0-9])$`)

// validName reports whether name is the name of a Python package.
func validName(name string) bool {
	return nameRE.MatchString(name)
}

// separatorsRE matches what normalize turns into "-".
var separatorsRE = regexp.MustCompile(`[-_.]+`)

// normalize returns a package name as uv shows it: in lower case, with
// "-" in place of runs of "-", "_" and ".".
func normalize(name string) string {
	return separatorsRE.ReplaceAllString(strings.ToLower(name), "-")
}

// Remover uninstalls the tools taken out of the config.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "uv-tool".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the tool rec records is still there to
// uninstall.
func (Remover) CheckRemove(_ context.Context, _ *resource.Runtime, rec state.Record) (resource.Change, error) {
	present, err := installed(rec)
	if err != nil || !present {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Remove, Summary: strings.TrimSpace(recordName(rec) + " " + rec.Details["version"])}, nil
}

// Remove uninstalls the tool rec records with uv tool uninstall, then
// deletes what's left of its environment and executables, which is all of
// them if uv is gone.
func (Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	name := recordName(rec)
	uv := rec.Details["uv"]
	if _, err := os.Stat(uv); uv == "" || err != nil {
		uv = "uv"
	}
	res, err := runUV(ctx, rt.Runner, uv, nil, []int{2}, "tool", "uninstall", name)
	switch {
	case errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
		// uv is gone.
	case err != nil:
		return err
	case res.ExitCode == 2 && !strings.Contains(string(res.Stderr), "is not installed"):
		return system.CheckExit(system.Cmd{Name: uv, Label: "uv tool uninstall"}, res)
	}

	dir := rec.Details["dir"]
	if dir == "" {
		return nil
	}
	for _, p := range rec.Paths {
		if into(p, dir) {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return os.RemoveAll(dir)
}

// installed reports whether anything of the tool rec records is left: its
// environment, or an executable linked into it. Without a recorded
// environment, only uv can tell, so it's assumed to be.
func installed(rec state.Record) (bool, error) {
	dir := rec.Details["dir"]
	if dir == "" {
		return true, nil
	}
	if _, err := os.Lstat(dir); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for _, p := range rec.Paths {
		if into(p, dir) {
			return true, nil
		}
	}
	return false, nil
}

// into reports whether p is a symlink to something in dir, as uv links a
// tool's executables into its environment.
func into(p, dir string) bool {
	target, err := os.Readlink(p)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	rel, err := filepath.Rel(dir, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// recordName returns the name of the tool rec records.
func recordName(rec state.Record) string {
	if name := rec.Details["name"]; name != "" {
		return name
	}
	name, _ := strings.CutPrefix(rec.ID, Kind+":")
	return name
}
