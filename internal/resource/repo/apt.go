package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
)

const (
	// sourcesDir holds apt's deb822 sources.
	sourcesDir = "/etc/apt/sources.list.d"
	// keyringsDir holds the keys apt's sources are signed with.
	keyringsDir = "/etc/apt/keyrings"
	// armorHeader starts an ASCII-armored OpenPGP public key.
	armorHeader = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
)

// AptSource is an apt source (Ubuntu), written in deb822 format to
// /etc/apt/sources.list.d/NAME.sources, with the key its repo is signed
// with in /etc/apt/keyrings/NAME.asc. The plan shows it as "repo NAME".
type AptSource struct {
	// Name names the repo, and its files.
	Name string
	// URIs, Suites, Components and Architectures are the source's fields
	// of the same names. Architectures may be empty.
	URIs, Suites, Components, Architectures []string
	// Key is the key the repo is signed with.
	Key Key

	// root is where the file system starts: "" means "/".
	root string
}

// Key is an OpenPGP public key a repo is signed with, ASCII-armored.
type Key struct {
	// Data is the key. If it's empty, the key is downloaded from URL.
	Data []byte
	// URL is where to download the key from, and SHA256 the sha256 in hex
	// the download must have.
	URL, SHA256 string
}

var _ resource.Resource = (*AptSource)(nil)

// ID is "repo:NAME".
func (s *AptSource) ID() string { return Kind + ":" + s.Name }

// Kind is Kind.
func (s *AptSource) Kind() string { return Kind }

// Targets are the source file and the key.
func (s *AptSource) Targets() []string { return []string{s.sourceFile(), s.keyFile()} }

// Privileged is true: the files belong to root.
func (s *AptSource) Privileged() bool { return true }

func (s *AptSource) sourceFile() string { return sourcesDir + "/" + s.Name + ".sources" }
func (s *AptSource) keyFile() string    { return keyringsDir + "/" + s.Name + ".asc" }

// render returns the source file.
func (s *AptSource) render() []byte {
	var b strings.Builder
	b.WriteString("# Written by maisys, which replaces any changes made here.\n")
	b.WriteString("Types: deb\n")
	field := func(name string, values []string) {
		if len(values) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(values, " "))
		}
	}
	field("URIs", s.URIs)
	field("Suites", s.Suites)
	field("Components", s.Components)
	field("Architectures", s.Architectures)
	field("Signed-By", []string{s.keyFile()})
	return []byte(b.String())
}

// check checks the source's settings.
func (s *AptSource) check(rt *resource.Runtime) error {
	if err := checkName(s.Name, rt, config.Ubuntu); err != nil {
		return err
	}
	switch {
	case len(s.URIs) == 0 || len(s.Suites) == 0:
		return fmt.Errorf("repo %s needs URIs and Suites", s.Name)
	case len(s.Key.Data) > 0:
		if !strings.HasPrefix(strings.TrimSpace(string(s.Key.Data)), armorHeader) {
			return fmt.Errorf("the key of repo %s isn't an ASCII-armored OpenPGP public key", s.Name)
		}
	case s.Key.URL == "":
		return fmt.Errorf("repo %s has no key", s.Name)
	default:
		return checkSHA256(s.Key.SHA256)
	}
	return nil
}

// state reports whether the source file and the key are as wanted, and
// whether the source file exists.
func (s *AptSource) state() (sourceOK, keyOK, exists bool, err error) {
	sourceOK, exists, err = sameFile(s.root, s.sourceFile(), s.render())
	if err != nil {
		return false, false, false, err
	}
	if len(s.Key.Data) > 0 {
		keyOK, _, err = sameFile(s.root, s.keyFile(), s.Key.Data)
	} else {
		keyOK, err = hasSHA256(s.root, s.keyFile(), s.Key.SHA256)
	}
	return sourceOK, keyOK, exists, err
}

// Check compares the source file and the key with what's wanted. It never
// downloads the key: a key from a URL is compared by its sha256.
func (s *AptSource) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := s.check(rt); err != nil {
		return resource.Change{}, err
	}
	sourceOK, keyOK, exists, err := s.state()
	switch {
	case err != nil:
		return resource.Change{}, err
	case !exists:
		return resource.Change{Action: resource.Create, Summary: "add repo " + s.Name}, nil
	case !sourceOK || !keyOK:
		return resource.Change{Action: resource.Update, Summary: "update repo " + s.Name}, nil
	}
	return resource.Change{}, nil
}

// Apply writes the key and the source file, where they aren't as wanted,
// downloading the key if it comes from a URL.
func (s *AptSource) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := s.check(rt); err != nil {
		return state.Record{}, err
	}
	m, err := pkg.ManagerFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	sourceOK, keyOK, _, err := s.state()
	if err != nil {
		return state.Record{}, err
	}
	if !keyOK {
		key := s.Key.Data
		if len(key) == 0 {
			if key, err = download(ctx, s.Key.URL, s.Key.SHA256); err != nil {
				return state.Record{}, fmt.Errorf("downloading the key of repo %s from %s: %w", s.Name, s.Key.URL, err)
			}
			if !strings.HasPrefix(strings.TrimSpace(string(key)), armorHeader) {
				return state.Record{}, fmt.Errorf("the key of repo %s from %s isn't an ASCII-armored OpenPGP public key", s.Name, s.Key.URL)
			}
		}
		if err := makeDir(ctx, rt, s.root, keyringsDir); err != nil {
			return state.Record{}, err
		}
		if err := writeFile(ctx, rt, s.root, s.keyFile(), key); err != nil {
			return state.Record{}, err
		}
	}
	if !sourceOK {
		if err := writeFile(ctx, rt, s.root, s.sourceFile(), s.render()); err != nil {
			return state.Record{}, err
		}
	}
	m.SourcesChanged()
	return state.Record{Details: map[string]string{"type": typeAptSource}}, nil
}

// hasSHA256 reports whether the file name under root has the sha256 sum.
func hasSHA256(root, name, sum string) (bool, error) {
	data, err := os.ReadFile(fsPath(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sha256Of(data) == strings.ToLower(sum), nil
}
