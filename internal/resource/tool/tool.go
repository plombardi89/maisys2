// Package tool installs tools from their upstream's releases, each version
// in a folder of its own under ~/.local/share/maisys/tools/<name>/, with
// symlinks in ~/.local/bin pointing at the version in use. Modules describe
// each tool with a Spec; this package has one for every tool maisys
// installs this way.
package tool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/version"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"

	"github.com/plombardi89/maisys2/internal/buildinfo"
)

// Kind is the kind of resource a Tool is.
const Kind = "tool"

// Marker is the file written last in a version's folder: a version counts
// as installed only once it's there.
const Marker = ".maisys-complete"

// Spec describes how to install a tool from its upstream's releases.
type Spec struct {
	// Name names the tool: its folder in ~/.local/share/maisys/tools, its
	// resource's ID ("tool:<name>"), and how the plan shows it.
	Name string
	// Latest finds the newest stable version, for version "latest".
	Latest version.Resolver
	// Arch is the upstream's name for each architecture in its file
	// names, such as "x86_64" for amd64. An architecture that's missing
	// has no download.
	Arch map[config.Arch]string
	// Asset returns the download for a version (without a leading "v"),
	// given the upstream's name for the architecture.
	Asset func(version, arch string) download.Asset
	// Checksum returns where the download's sha256 comes from.
	Checksum func(version string, a download.Asset) download.Checksum
	// Archive says how to unpack the download.
	Archive download.Archive
	// Links are the tool's commands, put in ~/.local/bin.
	Links []Link
}

// Link is one of a tool's commands: a symlink in ~/.local/bin to a file in
// the folder of the version in use.
type Link struct {
	// Name is the command's name, such as "gofmt".
	Name string
	// Target is the file it runs, relative to the version's folder and
	// with slashes, such as "go/bin/gofmt".
	Target string
}

// check reports a spec that can't work.
func (s *Spec) check() error {
	switch {
	case !validName(s.Name):
		return fmt.Errorf("a tool has an invalid name, %q", s.Name)
	case s.Asset == nil || s.Checksum == nil:
		return fmt.Errorf("tool %s has no download or no checksum source", s.Name)
	case len(s.Links) == 0:
		return fmt.Errorf("tool %s has no commands", s.Name)
	}
	for _, l := range s.Links {
		if !validName(l.Name) || l.Target == "" || filepath.IsAbs(l.Target) || strings.HasPrefix(filepath.Clean(filepath.FromSlash(l.Target)), "..") {
			return fmt.Errorf("tool %s has an invalid command, %s → %s", s.Name, l.Name, l.Target)
		}
	}
	return nil
}

// validName reports whether name can be a file name of its own.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, `/\`)
}

// Env is what the tools of a sync share: where tools go, and the client
// and caches downloads use. Every tool of a sync should share one, so each
// cache file is read once.
type Env struct {
	// Client downloads files and looks up their checksums.
	Client *download.Client
	// Latest looks up the latest versions, once per run, and keeps a dry
	// run's for the sync that follows it. Nil means they're looked up
	// every time.
	Latest *version.Cache
	// ToolsDir holds each tool's versions: ~/.local/share/maisys/tools.
	ToolsDir string
	// BinDir holds the commands: ~/.local/bin.
	BinDir string
}

// NewEnv returns the Env for the tools of a sync, with maisys's folders in
// paths, sending userAgent (such as "maisys/0.6.0") with every request.
// For a dry run, set Latest.DryRun.
func NewEnv(paths settings.Paths, userAgent string) *Env {
	return &Env{
		Client: &download.Client{
			HTTP:      &http.Client{},
			UserAgent: userAgent,
			Dir:       filepath.Join(paths.CacheDir, "downloads"),
			Digests:   &download.Digests{Path: filepath.Join(paths.CacheDir, "digests.json")},
		},
		Latest:   &version.Cache{Path: filepath.Join(paths.CacheDir, "latest.json")},
		ToolsDir: ToolsDir(paths.Home),
		BinDir:   BinDir(paths.Home),
	}
}

// EnvKey is the key the Env of a sync is shared under (see
// resource.Runtime.Shared). The sync makes it, and loads its caches, before
// it plans.
type EnvKey struct{}

// EnvFor returns the Env the resources of rt's sync share. If the sync
// didn't make one, it's made from rt.Paths.
func EnvFor(rt *resource.Runtime) (*Env, error) {
	v, err := rt.Shared(EnvKey{}, func() (any, error) {
		return NewEnv(rt.Paths, "maisys/"+buildinfo.Version()), nil
	})
	if err != nil {
		return nil, err
	}
	env, _ := v.(*Env)
	return env, nil
}

// Load reads the caches, so that one written by a newer maisys stops a sync
// before it plans anything.
func (e *Env) Load() error {
	if e.Latest != nil {
		if err := e.Latest.Load(); err != nil {
			return err
		}
	}
	if e.Client != nil && e.Client.Digests != nil {
		return e.Client.Digests.Load()
	}
	return nil
}

// ToolsDir returns the folder that holds each tool's versions, given the
// home folder.
func ToolsDir(home string) string {
	return filepath.Join(home, ".local", "share", "maisys", "tools")
}

// BinDir returns the folder that holds the tools' commands, given the home
// folder.
func BinDir(home string) string {
	return filepath.Join(home, ".local", "bin")
}

// Tool installs a tool at a version: it downloads the release for the
// machine's architecture, verifies its checksum, unpacks it into
// ~/.local/share/maisys/tools/<name>/<version>/, writes the Marker last,
// then points the tool's commands in ~/.local/bin at it, replacing each
// symlink in one step. A version counts as installed only once its marker
// exists, its folder has every command's file, and every command points at
// it. Once the commands point at a new version, the tool's other versions
// are deleted.
//
// A file at a command's path that isn't a symlink into the tool's folder
// is backed up to <path>.maisys-bak.<time> before it's replaced.
type Tool struct {
	// Spec describes the tool.
	Spec Spec
	// Version is the version the config asks for: "latest" (or "") for the
	// newest stable release, or an exact version.
	Version string
	// Env is shared by every tool of the sync.
	Env *Env

	// What Check found, for Apply.
	checked bool
	want    string
	asset   download.Asset
	sum     string
}

var _ resource.Resource = (*Tool)(nil)

// New returns a Tool installing spec at version v, with env.
func New(spec Spec, v string, env *Env) *Tool {
	return &Tool{Spec: spec, Version: v, Env: env}
}

// ID returns "tool:<name>".
func (t *Tool) ID() string { return Kind + ":" + t.Spec.Name }

// Kind returns Kind.
func (t *Tool) Kind() string { return Kind }

// Targets returns the tool's folder and its commands.
func (t *Tool) Targets() []string {
	targets := []string{t.dir()}
	for _, l := range t.Spec.Links {
		targets = append(targets, t.linkPath(l))
	}
	return targets
}

// Privileged returns false: tools are installed in the home folder.
func (t *Tool) Privileged() bool { return false }

// latest reports whether the config asks for the latest version.
func (t *Tool) latest() bool {
	return t.Version == "" || t.Version == config.Latest
}

// dir returns the tool's folder, which holds its versions.
func (t *Tool) dir() string {
	return filepath.Join(t.Env.ToolsDir, t.Spec.Name)
}

// versionDir returns the folder of version v.
func (t *Tool) versionDir(v string) string {
	return filepath.Join(t.dir(), v)
}

// linkPath returns where command l goes.
func (t *Tool) linkPath(l Link) string {
	return filepath.Join(t.Env.BinDir, l.Name)
}

// linkTarget returns the file command l runs in version v.
func (t *Tool) linkTarget(l Link, v string) string {
	return filepath.Join(t.versionDir(v), filepath.FromSlash(l.Target))
}

// Check finds the version to install, looking up the latest one if the
// config asks for it (a failed lookup is a *version.LookupError), and
// compares it with what's installed. If the version needs downloading, it
// also looks up its checksum, so a download that can't be verified fails
// the plan.
func (t *Tool) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if t.Env == nil {
		return resource.Change{}, fmt.Errorf("tool %s has no environment", t.Spec.Name)
	}
	if err := t.Spec.check(); err != nil {
		return resource.Change{}, err
	}
	want, err := t.resolve(ctx)
	if err != nil {
		return resource.Change{}, err
	}
	t.want, t.asset, t.sum = want, download.Asset{}, ""

	st := t.inspect(want)
	var c resource.Change
	latest := ""
	if t.latest() {
		latest = " (latest)"
	}
	switch {
	case st.complete && st.linked && len(st.leftovers) == 0:
		c.Action = resource.NoChange
	case st.complete && st.linked:
		c.Action = resource.Update
		c.Summary = describe(t.Spec.Name, want) + " (delete " + describeLeftovers(st.leftovers) + ")"
	case st.current != "" && st.current != want:
		c.Action = resource.Update
		c.Summary = describe(t.Spec.Name, st.current) + " → " + want + latest
	case st.complete:
		c.Action = resource.Update
		c.Summary = describe(t.Spec.Name, want) + " (restore commands)"
	default:
		c.Action = resource.Create
		c.Summary = describe(t.Spec.Name, want) + latest
	}
	for _, p := range st.foreign {
		c.Notes = append(c.Notes, fmt.Sprintf("%s isn't a link to a maisys tool (will back up and replace)", display(rt, p)))
	}

	if !st.complete {
		if t.asset, t.sum, err = t.checksum(ctx, rt, want); err != nil {
			return resource.Change{}, err
		}
	}
	t.checked = true
	return c, nil
}

// resolve returns the version to install, without a leading "v".
func (t *Tool) resolve(ctx context.Context) (string, error) {
	if !t.latest() {
		v := strings.TrimPrefix(t.Version, "v")
		if !config.IsExactVersion(v) {
			return "", fmt.Errorf("%q isn't a version of %s", t.Version, t.Spec.Name)
		}
		return v, nil
	}
	if t.Spec.Latest == nil {
		return "", fmt.Errorf("%s has no latest version: pin one", t.Spec.Name)
	}
	v, err := t.Env.Latest.Resolve(ctx, t.Env.Client, t.Spec.Name, t.Spec.Latest)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(v, "v"), nil
}

// checksum returns version v's download for the machine's architecture,
// and its sha256.
func (t *Tool) checksum(ctx context.Context, rt *resource.Runtime, v string) (download.Asset, string, error) {
	arch := config.Arch(runtime.GOARCH)
	if rt != nil && rt.Facts != nil && rt.Facts.Arch != "" {
		arch = rt.Facts.Arch
	}
	name, ok := t.Spec.Arch[arch]
	if !ok {
		return download.Asset{}, "", fmt.Errorf("%s has no download for %s", t.Spec.Name, arch)
	}
	a := t.Spec.Asset(v, name)
	sum, err := t.Spec.Checksum(v, a).SHA256(ctx, t.Env.Client, a)
	if nc, ok := errors.AsType[*download.NoChecksumError](err); ok {
		return a, "", fmt.Errorf("%s %s has no checksum: %s", t.Spec.Name, v, nc.Reason)
	}
	if err != nil {
		return a, "", fmt.Errorf("looking up the checksum of %s %s: %w", t.Spec.Name, v, err)
	}
	return a, sum, nil
}

// status is what's installed, compared with the version wanted.
type status struct {
	// complete reports whether the wanted version is installed (see
	// Tool.complete).
	complete bool
	// linked reports whether every command points at the wanted version.
	linked bool
	// current is the version every command points at, if they all point
	// at the same one, and it has its marker.
	current string
	// foreign are the commands' paths that hold something other than a
	// symlink into the tool's folder.
	foreign []string
	// leftovers are the entries of the tool's folder other than the
	// wanted version's: other versions, and unfinished installs.
	leftovers []string
}

// inspect finds what's installed.
func (t *Tool) inspect(want string) status {
	st := status{complete: t.complete(want), linked: true}
	// exact maps each version a command points into to whether every
	// such command points at its own file in it.
	exact := map[string]bool{}
	for _, l := range t.Spec.Links {
		path := t.linkPath(l)
		target, err := os.Readlink(path)
		if err != nil {
			st.linked = false
			if _, err := os.Lstat(path); err == nil {
				st.foreign = append(st.foreign, path)
			}
			continue
		}
		if target != t.linkTarget(l, want) {
			st.linked = false
		}
		v, ok := t.versionOf(target)
		if !ok {
			st.foreign = append(st.foreign, path)
			continue
		}
		prev, seen := exact[v]
		exact[v] = (prev || !seen) && target == t.linkTarget(l, v)
	}
	if len(exact) == 1 {
		for v, ok := range exact {
			if ok && hasMarker(t.versionDir(v)) {
				st.current = v
			}
		}
	}
	entries, _ := os.ReadDir(t.dir())
	for _, e := range entries {
		if e.Name() != want {
			st.leftovers = append(st.leftovers, e.Name())
		}
	}
	return st
}

// versionOf returns the version whose folder a symlink's target is in, if
// it's in the tool's folder.
func (t *Tool) versionOf(target string) (string, bool) {
	rel, ok := inside(target, t.dir())
	if !ok {
		return "", false
	}
	v, _, _ := strings.Cut(rel, string(filepath.Separator))
	return v, v != ""
}

// inside returns path relative to dir, if it's inside dir.
func inside(path, dir string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// complete reports whether version v is installed: its folder has its
// marker, and every command's file. A version an earlier maisys installed
// with a different spec for the tool, such as another Strip, may lack
// them, and is then installed again.
func (t *Tool) complete(v string) bool {
	if !hasMarker(t.versionDir(v)) {
		return false
	}
	for _, l := range t.Spec.Links {
		if _, err := os.Stat(t.linkTarget(l, v)); err != nil {
			return false
		}
	}
	return true
}

// hasMarker reports whether the version folder dir has its marker.
func hasMarker(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, Marker))
	return err == nil && info.Mode().IsRegular()
}

// describeLeftovers describes the entries of a tool's folder to delete:
// the old versions, or an unfinished install.
func describeLeftovers(names []string) string {
	var versions []string
	for _, name := range names {
		if !strings.HasPrefix(name, ".") {
			versions = append(versions, name)
		}
	}
	if len(versions) == 0 {
		return "an unfinished install"
	}
	return strings.Join(versions, ", ")
}

// Apply installs the version Check found unless it's installed already,
// points the tool's commands at it, and deletes the tool's other versions.
func (t *Tool) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if !t.checked {
		if _, err := t.Check(ctx, rt); err != nil {
			return state.Record{}, err
		}
	}
	if !t.complete(t.want) {
		if err := t.install(ctx, rt); err != nil {
			return state.Record{}, err
		}
	}
	if err := t.link(rt); err != nil {
		return state.Record{}, err
	}
	if err := t.prune(); err != nil {
		return state.Record{}, err
	}

	rec := state.Record{
		Paths:   []string{t.versionDir(t.want)},
		Details: map[string]string{"version": t.want},
	}
	for _, l := range t.Spec.Links {
		rec.Paths = append(rec.Paths, t.linkPath(l))
	}
	return rec, nil
}

// install downloads and unpacks the wanted version, and writes its marker.
func (t *Tool) install(ctx context.Context, rt *resource.Runtime) error {
	v := t.want
	if t.sum == "" {
		var err error
		if t.asset, t.sum, err = t.checksum(ctx, rt, v); err != nil {
			return err
		}
	}
	src, err := t.Env.Client.Fetch(ctx, t.asset, t.sum)
	if err != nil {
		return err
	}

	dir := t.versionDir(v)
	// A folder that isn't complete is an unfinished install, or one made
	// with an earlier spec.
	if err := download.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting the unfinished install of %s %s: %w", t.Spec.Name, v, err)
	}
	if err := t.Spec.Archive.Unpack(src, dir); err != nil {
		return fmt.Errorf("installing %s %s: %w", t.Spec.Name, v, err)
	}
	for _, l := range t.Spec.Links {
		if _, err := os.Stat(t.linkTarget(l, v)); err != nil {
			_ = download.RemoveAll(dir)
			return fmt.Errorf("%s %s's download has no %s", t.Spec.Name, v, l.Target)
		}
	}
	marker := fmt.Sprintf("%s %s\n%s  %s\n", t.Spec.Name, v, t.sum, t.asset.Name)
	if err := os.WriteFile(filepath.Join(dir, Marker), []byte(marker), 0o644); err != nil {
		return fmt.Errorf("installing %s %s: %w", t.Spec.Name, v, err)
	}
	// The download isn't needed once it's installed.
	if err := t.Env.Client.Forget(t.asset, t.sum); err != nil && rt != nil && rt.Log != nil {
		_, _ = fmt.Fprintf(rt.Log, "%v\n", err)
	}
	return nil
}

// link points every command at the wanted version, and deletes the
// commands an earlier version of the tool had that it no longer has.
func (t *Tool) link(rt *resource.Runtime) error {
	if err := os.MkdirAll(t.Env.BinDir, 0o755); err != nil {
		return err
	}
	var paths []string
	for _, l := range t.Spec.Links {
		path, target := t.linkPath(l), t.linkTarget(l, t.want)
		paths = append(paths, path)
		if current, err := os.Readlink(path); err == nil && current == target {
			continue
		}
		if err := t.backUp(rt, path); err != nil {
			return err
		}
		if err := symlink(target, path); err != nil {
			return fmt.Errorf("linking %s: %w", display(rt, path), err)
		}
	}

	if rt == nil || rt.Previous == nil {
		return nil
	}
	rec, ok := rt.Previous(t.ID())
	if !ok || len(rec.Paths) < 2 {
		return nil
	}
	for _, path := range rec.Paths[1:] {
		if slices.Contains(paths, path) || !t.ours(path) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("deleting %s: %w", display(rt, path), err)
		}
	}
	return nil
}

// ours reports whether path is a symlink into the tool's folder.
func (t *Tool) ours(path string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	_, ok := t.versionOf(target)
	return ok
}

// backUp moves what's at path aside, unless it's nothing or a symlink into
// the tool's folder.
func (t *Tool) backUp(rt *resource.Runtime, path string) error {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) || t.ours(path) {
		return nil
	}
	now := time.Now
	if rt != nil && rt.Now != nil {
		now = rt.Now
	}
	backup := path + ".maisys-bak." + now().Format("20060102-150405")
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("backing up %s: %w", display(rt, path), err)
	}
	return nil
}

// symlink makes path a symlink to target in one step: it makes the
// symlink under a temporary name, then renames it over path.
func symlink(target, path string) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".maisys-new")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// prune deletes everything in the tool's folder but the wanted version.
func (t *Tool) prune() error {
	entries, err := os.ReadDir(t.dir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == t.want {
			continue
		}
		if err := download.RemoveAll(filepath.Join(t.dir(), e.Name())); err != nil {
			return fmt.Errorf("deleting %s %s: %w", t.Spec.Name, e.Name(), err)
		}
	}
	return nil
}

// display returns path as the plan shows it: starting with ~/ if it's in
// the home folder.
func display(rt *resource.Runtime, path string) string {
	if rt == nil || rt.Paths.Home == "" {
		return path
	}
	if rel, ok := inside(path, rt.Paths.Home); ok {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

// describe returns how the plan names a tool's version, such as
// "kubectl 1.36.0".
func describe(name, v string) string {
	return strings.TrimSpace(name + " " + v)
}
