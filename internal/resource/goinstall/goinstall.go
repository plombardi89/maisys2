// Package goinstall keeps Go programs installed with go install, such as
// the golang module's tools, at an exact version or the latest one.
//
// A program is compared with the module version recorded in the installed
// binary (go version -m), and reinstalled only when the version wanted is
// different, so a sync that changes nothing runs no go install. What
// "latest" means comes from a resolver the module provides (the Go module
// proxy, honouring GOPROXY and GOPRIVATE).
//
// go runs with the user's environment, so GOPROXY, GOPRIVATE, GOBIN and
// GOPATH are kept, plus GOTOOLCHAIN=local, so the toolchain maisys
// manages is the one that builds the program.
package goinstall

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Program is.
const Kind = "go-install"

// Latest is the version that means the newest release.
const Latest = "latest"

// Program is a Go program installed with go install. Its ID is
// "go-install:" followed by its package path.
type Program struct {
	// Package is the path of the program's main package, such as
	// "github.com/go-task/task/v3/cmd/task".
	Package string
	// Version is the version to install: an exact version, such as
	// "v3.41.0", or "latest".
	Version string
	// Resolve returns the version "latest" means for the package, such as
	// from the Go module proxy. It's needed when Version is "latest", and
	// called by Check, or by Apply if Check couldn't look it up.
	Resolve func(ctx context.Context, pkg string) (string, error)
	// Go is the go binary to run: the toolchain maisys manages. Empty
	// means go from PATH. If the binary doesn't exist yet, as when the
	// same sync installs the toolchain first, Check can't look at the
	// installed program and plans to install it. If looking up "latest"
	// fails then too, as it does when the lookup needs go, Apply looks it
	// up once the toolchain is installed.
	Go string
	// Env is extra environment for go, as NAME=value.
	Env []string

	// version is the version to install, resolved by Check, or empty if
	// Apply is to resolve it.
	version string
}

var _ resource.Resource = (*Program)(nil)

// ID returns "go-install:" followed by the package path.
func (p *Program) ID() string { return Kind + ":" + p.Package }

// Kind returns "go-install".
func (p *Program) Kind() string { return Kind }

// Targets returns nothing: where the program goes depends on go's
// settings, which only Check and Apply read.
func (p *Program) Targets() []string { return nil }

// Privileged returns false: go install runs as the user.
func (p *Program) Privileged() bool { return false }

// Name returns the name of the program's binary, as go install names it:
// the last element of the package path, or the one before it if that's a
// major version suffix such as "v3".
func (p *Program) Name() string {
	return execName(p.Package)
}

// Check compares the version recorded in the installed binary with the
// version wanted. A failed lookup of "latest" is an error, unless the go
// binary doesn't exist yet: the lookup may need it, so the program is to
// be installed at whatever version Apply finds.
func (p *Program) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := p.validate(); err != nil {
		return resource.Change{}, err
	}
	name, haveGo := p.Name(), p.haveGo()
	p.version = ""
	v, err := p.resolve(ctx)
	if err != nil {
		if p.Version != Latest || haveGo {
			return resource.Change{}, err
		}
		if rt.Log != nil {
			_, _ = fmt.Fprintf(rt.Log, "go install %s: looking up the latest version once go is installed: %v\n", p.Package, err)
		}
		return resource.Change{Action: resource.Create, Summary: name + " (latest)"}, nil
	}
	p.version = v
	wanted := v
	if p.Version == Latest {
		wanted += " (latest)"
	}
	if !haveGo {
		return resource.Change{Action: resource.Create, Summary: name + " " + wanted}, nil
	}

	bin, err := binary(ctx, rt.Runner, p.goBinary(), p.Package, p.Env)
	if err != nil {
		return resource.Change{}, err
	}
	info, err := inspect(ctx, rt.Runner, p.goBinary(), bin, p.Env)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resource.Change{Action: resource.Create, Summary: name + " " + wanted}, nil
	case errors.Is(err, errNotGo):
		return resource.Change{Action: resource.Warn, Summary: fmt.Sprintf("%s: %s isn't a Go program (will be replaced)", name, tilde(rt.Paths.Home, bin))}, nil
	case err != nil:
		return resource.Change{}, err
	case info.path != p.Package:
		return resource.Change{Action: resource.Warn, Summary: fmt.Sprintf("%s: %s is %s (will be replaced)", name, tilde(rt.Paths.Home, bin), info.path)}, nil
	case info.version == v:
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("%s %s → %s", name, cmp.Or(info.version, "(unknown version)"), wanted)}, nil
}

// validate checks the program's fields.
func (p *Program) validate() error {
	switch {
	case p.Package == "" || strings.HasPrefix(p.Package, "-") || strings.ContainsAny(p.Package, "@ \t\r\n"):
		return fmt.Errorf("%q isn't a Go package path", p.Package)
	case p.Version == "" || strings.HasPrefix(p.Version, "-") || strings.ContainsAny(p.Version, "@ \t\r\n"):
		return fmt.Errorf("%s: %q isn't a version", p.Package, p.Version)
	case p.Version == Latest && p.Resolve == nil:
		return fmt.Errorf("%s: nothing says what the latest version is", p.Package)
	}
	return nil
}

// resolve returns the version to install.
func (p *Program) resolve(ctx context.Context) (string, error) {
	if p.Version != Latest {
		return p.Version, nil
	}
	v, err := p.Resolve(ctx, p.Package)
	if err != nil {
		return "", fmt.Errorf("couldn't find the latest version of %s: %w", p.Package, err)
	}
	if v == "" || v == Latest || strings.HasPrefix(v, "-") || strings.ContainsAny(v, "@ \t\r\n") {
		return "", fmt.Errorf("the latest version of %s is %q, which isn't a version", p.Package, v)
	}
	return v, nil
}

// goBinary returns the go binary to run.
func (p *Program) goBinary() string {
	return cmp.Or(p.Go, "go")
}

// haveGo reports whether the go binary exists. go from PATH is assumed
// to.
func (p *Program) haveGo() bool {
	if p.Go == "" {
		return true
	}
	_, err := os.Stat(p.Go)
	return err == nil
}

// Apply runs go install for the version wanted.
func (p *Program) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := p.validate(); err != nil {
		return state.Record{}, err
	}
	v := p.version
	if v == "" {
		var err error
		if v, err = p.resolve(ctx); err != nil {
			return state.Record{}, err
		}
	}
	_, err := rt.Runner.Run(ctx, system.Cmd{
		Name:  p.goBinary(),
		Args:  []string{"install", p.Package + "@" + v},
		Dir:   os.TempDir(),
		Env:   environ(p.Env),
		Label: "go install " + p.Package + "@" + v,
	})
	if err != nil {
		return state.Record{}, err
	}
	bin, err := binary(ctx, rt.Runner, p.goBinary(), p.Package, p.Env)
	if err != nil {
		return state.Record{}, err
	}
	details := map[string]string{"package": p.Package, "version": v}
	if p.Go != "" {
		details["go"] = p.Go
	}
	return state.Record{Paths: []string{bin}, Details: details}, nil
}

// environ returns the environment for go: env, and a local toolchain.
func environ(env []string) []string {
	return append([]string{"GOTOOLCHAIN=local"}, env...)
}

// binary returns where go install puts the program pkg: GOBIN, or the bin
// folder of the first entry in GOPATH.
func binary(ctx context.Context, r system.Runner, goBin, pkg string, env []string) (string, error) {
	res, err := r.Run(ctx, system.Cmd{
		Name:  goBin,
		Args:  []string{"env", "GOBIN", "GOPATH", "GOEXE"},
		Dir:   os.TempDir(),
		Env:   environ(env),
		Label: "go env",
	})
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(res.Stdout), "\r\n", "\n"), "\n")
	if len(lines) < 3 {
		return "", fmt.Errorf("go env printed %q, not GOBIN, GOPATH and GOEXE", res.Stdout)
	}
	dir := lines[0]
	if dir == "" {
		gopath := filepath.SplitList(lines[1])
		if len(gopath) == 0 || gopath[0] == "" {
			return "", errors.New("go env doesn't say where go install puts programs: GOBIN and GOPATH are both empty")
		}
		dir = filepath.Join(gopath[0], "bin")
	}
	return filepath.Join(dir, execName(pkg)+lines[2]), nil
}

// errNotGo means a file isn't a Go program go version -m can read.
var errNotGo = errors.New("not a Go program")

// buildInfo is what go version -m found in a binary.
type buildInfo struct {
	// path is the main package's path.
	path string
	// module is the main module's path.
	module string
	// version is the main module's version, such as "v3.41.0" or
	// "(devel)".
	version string
}

// inspect reads the build information in bin. It fails with an error
// wrapping fs.ErrNotExist if bin doesn't exist, and errNotGo if it isn't a
// Go program.
func inspect(ctx context.Context, r system.Runner, goBin, bin string, env []string) (buildInfo, error) {
	if _, err := os.Lstat(bin); err != nil {
		return buildInfo{}, err
	}
	res, err := r.Run(ctx, system.Cmd{
		Name:      goBin,
		Args:      []string{"version", "-m", bin},
		Dir:       os.TempDir(),
		Env:       environ(env),
		Label:     "go version -m",
		AllowExit: []int{1},
	})
	if err != nil {
		return buildInfo{}, err
	}
	info := parseVersionM(string(res.Stdout))
	if res.ExitCode != 0 || info.path == "" {
		return buildInfo{}, errNotGo
	}
	return info, nil
}

// parseVersionM reads the output of go version -m for one binary:
//
//	/home/u/go/bin/task: go1.26.1
//		path	github.com/go-task/task/v3/cmd/task
//		mod	github.com/go-task/task/v3	v3.41.0	h1:…
//		dep	…
//		build	…
func parseVersionM(out string) buildInfo {
	var info buildInfo
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(fields) < 3 || fields[0] != "" {
			continue
		}
		switch fields[1] {
		case "path":
			info.path = fields[2]
		case "mod":
			info.module = fields[2]
			if len(fields) > 3 {
				info.version = fields[3]
			}
		}
	}
	return info
}

// execName returns the name go install gives the binary of package pkg.
func execName(pkg string) string {
	elem := path.Base(pkg)
	if isMajorVersion(elem) {
		elem = path.Base(path.Dir(pkg))
	}
	return elem
}

// isMajorVersion reports whether elem is a major version suffix of a
// module path: v2, v3 and so on, but not v0, v1 or v01.
func isMajorVersion(elem string) bool {
	if len(elem) < 2 || elem[0] != 'v' || elem[1] == '0' || elem == "v1" {
		return false
	}
	return !strings.ContainsFunc(elem[1:], func(r rune) bool { return r < '0' || r > '9' })
}

// Remover deletes the programs taken out of the config, if the binary is
// still the program maisys installed.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "go-install".
func (Remover) Kind() string { return Kind }

// CheckRemove checks whether the binary rec records is still there, and
// still the program installed: something else is left in place.
func (r Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	bin, info, why, err := r.inspect(ctx, rt, rec)
	switch {
	case err != nil || bin == "":
		return resource.Change{}, err
	case why != "":
		return resource.Change{Action: resource.Warn, Summary: fmt.Sprintf("%s %s (left in place)", tilde(rt.Paths.Home, bin), why)}, nil
	}
	return resource.Change{Action: resource.Remove, Summary: execName(info.path) + " " + info.version}, nil
}

// Remove deletes the binary rec records, if it's still the program
// installed.
func (r Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	bin, _, why, err := r.inspect(ctx, rt, rec)
	if err != nil || bin == "" || why != "" {
		return err
	}
	if err := os.Remove(bin); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// inspect returns the binary rec records and its build information, or ""
// if it's gone. If it's no longer the program installed, or can't be
// checked, why says so.
func (Remover) inspect(ctx context.Context, rt *resource.Runtime, rec state.Record) (bin string, info buildInfo, why string, err error) {
	if len(rec.Paths) == 0 {
		return "", buildInfo{}, "", nil
	}
	bin = rec.Paths[0]
	goBin := rec.Details["go"]
	if _, err := os.Stat(goBin); goBin == "" || err != nil {
		goBin = "go"
	}
	info, err = inspect(ctx, rt.Runner, goBin, bin, nil)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", buildInfo{}, "", nil
	case errors.Is(err, errNotGo):
		return bin, info, "isn't a Go program any more", nil
	case errors.Is(err, exec.ErrNotFound):
		return bin, info, "can't be checked without go", nil
	case err != nil:
		return "", info, "", err
	case info.path != rec.Details["package"]:
		return bin, info, "is " + info.path + " now", nil
	}
	return bin, info, "", nil
}

// tilde returns path with the home folder shown as ~.
func tilde(home, path string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}
