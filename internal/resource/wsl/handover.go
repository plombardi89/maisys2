package wsl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/selfupdate"
)

// ReleaseURLEnv names the environment variable that points self-update,
// and the Linux maisys handed to new distros, at the files of another
// release, such as a folder of CI-built files. It's for tests.
const ReleaseURLEnv = "MAISYS_RELEASE_URL"

// BinarySource finds the Linux maisys to copy into new distros.
type BinarySource interface {
	// Path returns the binary's path, and its sha256 in hex.
	Path(ctx context.Context) (path, sum string, err error)
}

// LinuxBinary is the Linux maisys that's copied into new distros: the one
// in the linux/amd64 archive of the running maisys's release, verified
// against the release's checksums.txt. The archive is kept in the download
// cache, so later distros don't download it again.
type LinuxBinary struct {
	// ReleaseURL is where the release's files are (http, https or file),
	// such as https://github.com/OWNER/REPO/releases/download/v0.6.0.
	ReleaseURL string
	// Version is the release's version, without a leading "v". Empty means
	// whichever linux/amd64 archive the release has, for development
	// builds, which aren't released.
	Version string
	// CacheDir is maisys's cache folder, %LOCALAPPDATA%\maisys\cache.
	CacheDir string
	// UserAgent is sent with every request, such as "maisys/0.6.0".
	UserAgent string
}

// NewLinuxBinary returns the LinuxBinary of the running maisys, keeping
// its files in cacheDir. The release is the one of the running version in
// the repo maisys updates from, or the files at $MAISYS_RELEASE_URL if
// that's set, as getenv reads it. A development build has no release, so
// it needs MAISYS_RELEASE_URL.
func NewLinuxBinary(cacheDir string, getenv func(string) string) (*LinuxBinary, error) {
	b := &LinuxBinary{
		ReleaseURL: getenv(ReleaseURLEnv),
		Version:    buildinfo.Version(),
		CacheDir:   cacheDir,
		UserAgent:  "maisys/" + buildinfo.Version(),
	}
	if buildinfo.IsDev() {
		b.Version = ""
		if b.ReleaseURL == "" {
			return nil, fmt.Errorf("this development build of maisys has no release to give new distros the Linux maisys of: set %s to the files of a build, such as dist/ from make snapshot", ReleaseURLEnv)
		}
	}
	if b.ReleaseURL == "" {
		b.ReleaseURL = selfupdate.ReleaseURL(buildinfo.UpdateRepo(), b.Version)
	}
	return b, nil
}

// Path returns where the Linux maisys is in the cache, and its sha256 in
// hex, downloading the archive first if the cache doesn't have it.
func (b *LinuxBinary) Path(ctx context.Context) (path, sum string, err error) {
	u := &selfupdate.Updater{
		ReleaseURL: b.ReleaseURL,
		OS:         "linux",
		Arch:       "amd64",
		Client:     &http.Client{Timeout: 5 * time.Minute},
		UserAgent:  b.UserAgent,
	}
	rel, err := u.Find(ctx, b.Version)
	if err != nil {
		return "", "", fmt.Errorf("finding the Linux maisys for new distros in %s: %w", b.ReleaseURL, err)
	}
	rel.SHA256 = strings.ToLower(rel.SHA256)
	if len(rel.SHA256) != sha256.Size*2 {
		return "", "", fmt.Errorf("checksums.txt in %s has an invalid sha256 for %s", b.ReleaseURL, rel.Archive)
	}

	archive := filepath.Join(b.CacheDir, "downloads", rel.SHA256[:16]+"-"+rel.Archive)
	data, err := os.ReadFile(archive)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	bin, err := selfupdate.Unpack(data, rel)
	if err != nil {
		// The cache doesn't have it, or not as it should be.
		if data, err = u.Fetch(ctx, rel.Archive); err != nil {
			return "", "", fmt.Errorf("downloading the Linux maisys for new distros: %w", err)
		}
		if bin, err = selfupdate.Unpack(data, rel); err != nil {
			return "", "", err
		}
		if err := writeFile(archive, data); err != nil {
			return "", "", err
		}
	}

	path = filepath.Join(b.CacheDir, "linux", "maisys-"+rel.Version)
	if cur, err := os.ReadFile(path); err != nil || !bytes.Equal(cur, bin) {
		if err := writeFile(path, bin); err != nil {
			return "", "", err
		}
	}
	h := sha256.Sum256(bin)
	return path, hex.EncodeToString(h[:]), nil
}

// writeFile writes data to path through a temporary file, so it's never
// left half written, creating its folder if it's missing.
func writeFile(path string, data []byte) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
