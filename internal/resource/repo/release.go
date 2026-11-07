package repo

import (
	"context"
	"fmt"
	"os"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
)

// ReleasePackage is a vendor's package that adds its repo, such as RPM
// Fusion's release RPM or Valve's steam .deb, installed from a URL with apt
// or dnf. It counts as there once the package is installed; maisys doesn't
// upgrade it, since the repo it adds does. The plan shows it as "repo
// NAME".
type ReleasePackage struct {
	// Name names the repo.
	Name string
	// Package is the name the package has once it's installed, such as
	// "rpmfusion-free-release".
	Package string
	// URL is where to download the package from, a .deb on Ubuntu and an
	// .rpm on Fedora, and SHA256 the sha256 in hex the download must have.
	URL, SHA256 string
}

var _ resource.Resource = (*ReleasePackage)(nil)

// ID is "repo:NAME".
func (r *ReleasePackage) ID() string { return Kind + ":" + r.Name }

// Kind is Kind.
func (r *ReleasePackage) Kind() string { return Kind }

// Targets is empty: the package owns its files.
func (r *ReleasePackage) Targets() []string { return nil }

// Privileged is true: installing packages needs root.
func (r *ReleasePackage) Privileged() bool { return true }

func (r *ReleasePackage) check(rt *resource.Runtime) error {
	if !validName.MatchString(r.Name) {
		return fmt.Errorf("%q isn't a valid repo name", r.Name)
	}
	if rt.Facts == nil || (rt.Facts.OS != config.Ubuntu && rt.Facts.OS != config.Fedora) {
		return fmt.Errorf("repo %s only works on Fedora and Ubuntu", r.Name)
	}
	if r.Package == "" || r.URL == "" {
		return fmt.Errorf("repo %s needs the package's name and URL", r.Name)
	}
	return checkSHA256(r.SHA256)
}

// Check finds whether the package is installed.
func (r *ReleasePackage) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := r.check(rt); err != nil {
		return resource.Change{}, err
	}
	m, err := pkg.ManagerFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	installed, err := m.Installed(ctx, r.Package)
	if err != nil || installed {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Create, Summary: fmt.Sprintf("add repo %s (install %s)", r.Name, r.Package)}, nil
}

// Apply downloads the package, checks its sha256, and installs it.
func (r *ReleasePackage) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := r.check(rt); err != nil {
		return state.Record{}, err
	}
	m, err := pkg.ManagerFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	data, err := download(ctx, r.URL, r.SHA256)
	if err != nil {
		return state.Record{}, fmt.Errorf("downloading %s from %s: %w", r.Package, r.URL, err)
	}
	ext := ".rpm"
	if rt.Facts.OS == config.Ubuntu {
		ext = ".deb"
	}
	file, err := writeTemp(data, ext)
	if err != nil {
		return state.Record{}, fmt.Errorf("saving %s: %w", r.Package, err)
	}
	defer func() { _ = os.Remove(file) }()
	if err := m.Install(ctx, file); err != nil {
		return state.Record{}, err
	}
	m.SourcesChanged()
	return state.Record{Details: map[string]string{"type": typeReleasePackage, "package": r.Package}}, nil
}

// writeTemp writes data to a new temporary file whose name ends with ext,
// and returns its path.
func writeTemp(data []byte, ext string) (string, error) {
	f, err := os.CreateTemp("", "maisys-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
