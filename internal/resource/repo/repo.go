// Package repo adds package repos: apt deb822 sources with their signing
// keys, dnf .repo files, vendor release packages that add a repo (such as
// RPM Fusion's), and extra dpkg architectures (such as i386 for Steam).
// Each one tells the sync's package Manager the package sources changed, so
// apt's lists are refreshed before packages are installed. maisys never
// removes repos: once one is taken out of the config, the plan says it's no
// longer used.
package repo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

const (
	// Kind is the kind of repo resources and their records: apt sources,
	// dnf repos and release packages.
	Kind = "repo"
	// ArchKind is the kind of Architecture resources and their records.
	ArchKind = "dpkg-arch"
)

// The types of repo records, in their "type" detail.
const (
	typeAptSource      = "apt-source"
	typeDnfRepo        = "dnf-repo"
	typeReleasePackage = "release-package"
)

// validName matches repo names, which name files.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// maxDownload is the most a download may be.
const maxDownload = 256 << 20

// client downloads keys and release packages.
var client = &http.Client{Timeout: 5 * time.Minute}

// checkName checks a repo's name, and that the machine runs want.
func checkName(name string, rt *resource.Runtime, want config.OS) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q isn't a valid repo name", name)
	}
	if rt.Facts == nil || rt.Facts.OS != want {
		return fmt.Errorf("repo %s only works on %s", name, osName(want))
	}
	return nil
}

// osName returns how messages name a Linux OS.
func osName(o config.OS) string {
	if o == config.Ubuntu {
		return "Ubuntu"
	}
	return "Fedora"
}

// fsPath returns where the slash-separated absolute path name is under
// root, which "" means is "/".
func fsPath(root, name string) string {
	if root == "" {
		root = string(filepath.Separator)
	}
	return filepath.Join(root, filepath.FromSlash(name))
}

// sameFile reports whether the file name under root holds data, and
// whether it exists.
func sameFile(root, name string, data []byte) (same, exists bool, err error) {
	have, err := os.ReadFile(fsPath(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return bytes.Equal(have, data), true, nil
}

// writeFile writes data to the file name under root, as root, readable by
// everyone.
func writeFile(ctx context.Context, rt *resource.Runtime, root, name string, data []byte) error {
	return system.InstallFile(ctx, rt.Runner, data, fsPath(root, name), 0o644)
}

// makeDir creates the folder name under root, as root, if it's missing.
func makeDir(ctx context.Context, rt *resource.Runtime, root, name string) error {
	dir := fsPath(root, name)
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	_, err := rt.Runner.Run(ctx, system.Cmd{
		Name:  "install",
		Args:  []string{"-d", "-m", "0755", "-o", "root", "-g", "root", dir},
		Root:  true,
		Label: "creating " + name,
	})
	return err
}

// checkSHA256 checks that sum is a sha256 in hex.
func checkSHA256(sum string) error {
	if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
		return fmt.Errorf("%q isn't a sha256", sum)
	}
	return nil
}

// sha256Of returns the sha256 of data, in hex.
func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// download downloads url, and checks it has the sha256 sum.
func download(ctx context.Context, url, sum string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server said %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("it's bigger than %d MiB", maxDownload>>20)
	}
	if got := sha256Of(data); got != strings.ToLower(sum) {
		return nil, fmt.Errorf("its sha256 is %s, not %s as expected", got, strings.ToLower(sum))
	}
	return data, nil
}

// Remover handles the records of repos taken out of the config. maisys
// never removes repos: one that's still there is reported as no longer
// used, and its record kept. The record of one that's gone is forgotten.
type Remover struct {
	// root is where the file system starts: "" means "/".
	root string
}

var _ resource.Remover = Remover{}

// Kind is Kind.
func (Remover) Kind() string { return Kind }

// CheckRemove reports the repo as no longer used, if it's still there.
func (r Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	var there bool
	if rec.Details["type"] == typeReleasePackage {
		m, err := pkg.ManagerFor(rt)
		if err != nil {
			return resource.Change{}, err
		}
		if there, err = m.Installed(ctx, rec.Details["package"]); err != nil {
			return resource.Change{}, err
		}
	} else {
		there = slices.ContainsFunc(rec.Paths, func(name string) bool {
			_, err := os.Stat(fsPath(r.root, name))
			return err == nil
		})
	}
	if !there {
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Orphan, Summary: resource.Describe(rec.ID)}, nil
}

// Remove fails: maisys never removes repos.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error {
	return errors.New("maisys never removes repos")
}
