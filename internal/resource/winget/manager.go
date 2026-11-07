package winget

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

// source is the winget source packages come from: the community
// repository.
const source = "winget"

// Manager runs winget for the package resources of a sync. It runs one
// winget command at a time, since winget isn't documented to be safe to
// run concurrently, and exports what's installed once. It's safe for
// concurrent use.
type Manager struct {
	runner system.Runner
	// tempDir is where winget export writes its file. Empty means the
	// system's temporary folder.
	tempDir string

	// mu is held while winget runs.
	mu sync.Mutex
	// installed maps the ID of each package installed from the source to
	// its version, once exported is set.
	installed map[string]string
	exported  bool
	// noUpgrades says why winget can't check for upgrades, once it's
	// found it can't.
	noUpgrades string

	wantMu sync.Mutex
	// wanted holds the IDs the sync's package sets want.
	wanted []string
}

// NewManager returns a Manager that runs winget with r.
func NewManager(r system.Runner) *Manager {
	return &Manager{runner: r}
}

// ManagerFor returns the Manager the resources of rt's sync share.
func ManagerFor(rt *resource.Runtime) (*Manager, error) {
	v, err := rt.Shared(managerKey{}, func() (any, error) {
		return NewManager(rt.Runner), nil
	})
	if err != nil {
		return nil, err
	}
	m, ok := v.(*Manager)
	if !ok {
		return nil, fmt.Errorf("the shared winget manager is a %T", v)
	}
	return m, nil
}

// managerKey is the key of the shared Manager.
type managerKey struct{}

// want notes ids a package set wants.
func (m *Manager) want(ids ...string) {
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	for _, id := range ids {
		if !slices.Contains(m.wanted, id) {
			m.wanted = append(m.wanted, id)
		}
	}
}

// wants reports whether a package set of this sync wants id.
func (m *Manager) wants(id string) bool {
	m.wantMu.Lock()
	defer m.wantMu.Unlock()
	return slices.Contains(m.wanted, id)
}

// run runs winget with args, one command at a time. m.mu must be held.
func (m *Manager) run(ctx context.Context, c system.Cmd) (system.Result, error) {
	c.Name = "winget"
	res, err := m.runner.Run(ctx, c)
	if errors.Is(err, exec.ErrNotFound) {
		err = fmt.Errorf("can't run winget, which comes with App Installer from the Microsoft Store: %w", err)
	}
	return res, err
}

// Installed reports whether the package id is installed from the winget
// source, and at which version: winget says "Unknown" for a package whose
// version it can't tell. The first call exports what's installed, for
// every later one.
func (m *Manager) Installed(ctx context.Context, id string) (version string, installed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.exported {
		installed, err := m.export(ctx)
		if err != nil {
			return "", false, err
		}
		m.installed, m.exported = installed, true
	}
	version, installed = m.installed[id]
	return version, installed, nil
}

// export runs winget export, and returns the ID and version of each
// package it lists as installed from the source. m.mu must be held.
func (m *Manager) export(ctx context.Context) (map[string]string, error) {
	dir, err := os.MkdirTemp(m.tempDir, "maisys-winget-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "packages.json")

	res, err := m.run(ctx, system.Cmd{
		Args: []string{
			"export", "--output", file, "--source", source, "--include-versions",
			"--accept-source-agreements", "--disable-interactivity",
		},
		AllowExit: exitCodes(noApplicationsFound), // nothing installed comes from the source
		Label:     "winget export",
	})
	if err != nil {
		return nil, commandError("", err)
	}
	if is(res, noApplicationsFound) {
		return map[string]string{}, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading what winget export wrote: %w", err)
	}
	installed, err := parseExport(data)
	if err != nil {
		return nil, fmt.Errorf("reading what winget export wrote: %w", err)
	}
	return installed, nil
}

// exportFile is the JSON winget export writes, as its schema
// (https://aka.ms/winget-packages.schema.2.0.json) describes it.
type exportFile struct {
	Schema  string `json:"$schema"`
	Sources []struct {
		Packages []struct {
			PackageIdentifier string `json:"PackageIdentifier"`
			Version           string `json:"Version"`
		} `json:"Packages"`
		SourceDetails struct {
			Name string `json:"Name"`
		} `json:"SourceDetails"`
	} `json:"Sources"`
}

// parseExport returns the ID and version of each package in data, a file
// winget export wrote, that comes from the source.
func parseExport(data []byte) (map[string]string, error) {
	var f exportFile
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &f); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(f.Schema, "https://aka.ms/winget-packages.schema.") {
		return nil, fmt.Errorf("it isn't a winget packages file: its $schema is %q", f.Schema)
	}
	installed := map[string]string{}
	for _, s := range f.Sources {
		if s.SourceDetails.Name != source {
			continue
		}
		for _, p := range s.Packages {
			if p.PackageIdentifier != "" {
				installed[p.PackageIdentifier] = p.Version
			}
		}
	}
	return installed, nil
}

// exists checks that the source has the package id, with winget show.
func (m *Manager) exists(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.run(ctx, system.Cmd{
		Args:      []string{"show", "--id", id, "--exact", "--source", source, "--accept-source-agreements", "--disable-interactivity"},
		AllowExit: exitCodes(noApplicationsFound),
		Label:     "winget show " + id,
	})
	if err != nil {
		return commandError(id, err)
	}
	if is(res, noApplicationsFound) {
		return fmt.Errorf("unknown winget package %s: the winget source doesn't have it (IDs are case-sensitive; find them with winget search)", id)
	}
	return nil
}

// upgradeCheck is whether an installed package has an upgrade.
type upgradeCheck struct {
	available bool
	// note says why it couldn't be checked, if it couldn't.
	note string
}

// dscPackage is the state of a package as WinGet's DSC v3 resource
// (winget dscv3 package) describes it.
type dscPackage struct {
	ID          string `json:"id"`
	Source      string `json:"source,omitempty"`
	MatchOption string `json:"matchOption,omitempty"`
	// Exist reports whether it's installed.
	Exist *bool `json:"_exist,omitempty"`
	// Version is the installed version.
	Version string `json:"version,omitempty"`
	// UseLatest reports whether the installed version is the latest
	// one: false means there's an upgrade.
	UseLatest *bool `json:"useLatest,omitempty"`
}

// upgradable checks whether the package id, installed at version, has an
// upgrade, with winget dscv3 package --get. A package whose version winget
// can't tell isn't checked: winget doesn't upgrade those either.
func (m *Manager) upgradable(ctx context.Context, id, version string) (upgradeCheck, error) {
	if version == "" || strings.EqualFold(version, "Unknown") {
		return upgradeCheck{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.noUpgrades != "" {
		return upgradeCheck{note: m.noUpgrades}, nil
	}

	input, err := json.Marshal(dscPackage{ID: id, Source: source, MatchOption: "equals"})
	if err != nil {
		return upgradeCheck{}, err
	}
	res, err := m.run(ctx, system.Cmd{
		Args:      []string{"dscv3", "package", "--get"},
		Stdin:     bytes.NewReader(input),
		AllowExit: exitCodes(invalidArguments, experimentalFeatureDisabled),
		Label:     "winget dscv3 package --get " + id,
	})
	if err != nil {
		return upgradeCheck{}, commandError(id, err)
	}
	if is(res, invalidArguments) || is(res, experimentalFeatureDisabled) {
		m.noUpgrades = "upgrades not checked: this winget is too old to say whether there are any (update App Installer from the Microsoft Store)"
		return upgradeCheck{note: m.noUpgrades}, nil
	}

	state, err := parseDSCOutput(res.Stdout)
	if err != nil {
		return upgradeCheck{}, fmt.Errorf("reading what winget dscv3 package --get said about %s: %w", id, err)
	}
	installed := state.Exist != nil && *state.Exist
	return upgradeCheck{available: installed && state.UseLatest != nil && !*state.UseLatest}, nil
}

// parseDSCOutput returns the package state in out, the output of winget
// dscv3 package --get: a line of JSON.
func parseDSCOutput(out []byte) (dscPackage, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if !bytes.HasPrefix(line, []byte("{")) {
			continue
		}
		var p dscPackage
		if err := json.Unmarshal(line, &p); err == nil {
			return p, nil
		}
	}
	if err := sc.Err(); err != nil {
		return dscPackage{}, err
	}
	return dscPackage{}, errors.New("it wrote no JSON")
}

// install installs the package s describes. An install that finds it
// already installed succeeds.
func (m *Manager) install(ctx context.Context, s Spec) error {
	args := []string{"install", "--id", s.ID, "--exact", "--source", source}
	if s.Machine {
		args = append(args, "--scope", "machine")
	}
	return m.change(ctx, s.ID, system.Cmd{
		Args:      append(args, "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"),
		AllowExit: exitCodes(packageAlreadyInstalled, updateNotApplicable),
		Label:     "winget install " + s.ID,
	})
}

// upgrade upgrades the package s describes, keeping the scope it was
// installed with. An upgrade that finds nothing newer succeeds.
func (m *Manager) upgrade(ctx context.Context, s Spec) error {
	return m.change(ctx, s.ID, system.Cmd{
		Args: []string{
			"upgrade", "--id", s.ID, "--exact", "--source", source,
			"--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity",
		},
		AllowExit: exitCodes(updateNotApplicable, upgradeVersionNotNewer),
		Label:     "winget upgrade " + s.ID,
	})
}

// change runs c, an install or upgrade of the package id, and forgets what
// was installed, which it changed.
func (m *Manager) change(ctx context.Context, id string, c system.Cmd) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.run(ctx, c)
	m.exported, m.installed = false, nil
	return commandError(id, err)
}
