// Package selfupdate replaces the running maisys binary with the latest
// release, if that release is newer.
//
// It reads checksums.txt from the latest release (the same file install.sh
// uses) to learn the latest version and its archive for this OS and
// architecture, verifies the archive's sha256, and swaps the new binary into
// place. It only ever moves forward.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// maxDownload caps how much is read from any one download.
const maxDownload = 256 << 20

// Release is a maisys release archive for one OS and architecture.
type Release struct {
	Version string // without a leading "v", for example "0.2.0"
	Archive string // file name, for example "maisys_0.2.0_linux_amd64.tar.gz"
	SHA256  string // hex-encoded
}

// Updater checks for and installs newer releases.
type Updater struct {
	// ReleaseURL is the base URL of the latest release's files: http, https
	// or file.
	ReleaseURL string
	// Current is the version of the running binary, without a leading "v".
	Current string
	// OS and Arch select the archive, using Go's names (runtime.GOOS and
	// runtime.GOARCH).
	OS, Arch string
	// Executable is the binary to replace.
	Executable string
	Client     *http.Client
	UserAgent  string
}

// DefaultReleaseURL returns the base URL of the latest release's files in a
// GitHub repo (owner/name). GitHub redirects it to the latest published
// release, which never includes drafts.
func DefaultReleaseURL(repo string) string {
	return "https://github.com/" + repo + "/releases/latest/download"
}

// Executable returns the path of the running binary, with symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Update installs the latest release if it's newer than the running binary.
// It returns the latest release and whether it was installed.
func (u *Updater) Update(ctx context.Context) (Release, bool, error) {
	if !semver.IsValid("v" + u.Current) {
		return Release{}, false, fmt.Errorf("running version %q isn't a release version", u.Current)
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		return Release{}, false, err
	}
	if semver.Compare("v"+rel.Version, "v"+u.Current) <= 0 {
		return rel, false, nil
	}
	if err := u.Install(ctx, rel); err != nil {
		return rel, false, err
	}
	return rel, true, nil
}

// Latest returns the latest release's archive for u.OS and u.Arch.
func (u *Updater) Latest(ctx context.Context) (Release, error) {
	data, err := u.fetch(ctx, "checksums.txt")
	if err != nil {
		return Release{}, err
	}
	prefix := "maisys_"
	suffix := "_" + u.OS + "_" + u.Arch + archiveExt(u.OS)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], fields[1]
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		if !semver.IsValid("v" + version) {
			return Release{}, fmt.Errorf("checksums.txt lists %s, which has an invalid version", name)
		}
		return Release{Version: version, Archive: name, SHA256: sum}, nil
	}
	if err := scanner.Err(); err != nil {
		return Release{}, fmt.Errorf("reading checksums.txt: %w", err)
	}
	return Release{}, fmt.Errorf("the latest release has no archive for %s/%s", u.OS, u.Arch)
}

// Install downloads rel, verifies its checksum and replaces u.Executable with
// the binary inside it.
func (u *Updater) Install(ctx context.Context, rel Release) error {
	data, err := u.fetch(ctx, rel.Archive)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, rel.SHA256) {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", rel.Archive, rel.SHA256, got)
	}

	bin, err := extract(data, rel.Archive, binaryName(u.OS))
	if err != nil {
		return fmt.Errorf("unpacking %s: %w", rel.Archive, err)
	}
	return replace(u.Executable, bin)
}

// fetch downloads the named file from u.ReleaseURL.
func (u *Updater) fetch(ctx context.Context, name string) ([]byte, error) {
	base, err := url.Parse(strings.TrimSuffix(u.ReleaseURL, "/") + "/" + name)
	if err != nil {
		return nil, fmt.Errorf("invalid release URL: %w", err)
	}

	var body io.ReadCloser
	switch base.Scheme {
	case "file":
		f, err := os.Open(fileURLPath(base))
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", name, err)
		}
		body = f
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return nil, err
		}
		if u.UserAgent != "" {
			req.Header.Set("User-Agent", u.UserAgent)
		}
		client := u.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", name, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("downloading %s: %s", base, resp.Status)
		}
		body = resp.Body
	default:
		return nil, fmt.Errorf("unsupported release URL scheme %q", base.Scheme)
	}
	defer func() { _ = body.Close() }()

	data, err := io.ReadAll(io.LimitReader(body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("downloading %s: larger than %d bytes", name, maxDownload)
	}
	return data, nil
}

// fileURLPath converts a file URL to a local path, including Windows drive
// letters (file:///C:/dir becomes C:\dir).
func fileURLPath(u *url.URL) string {
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func archiveExt(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "maisys.exe"
	}
	return "maisys"
}

// extract returns the contents of the file called name at the top of the
// archive.
func extract(data []byte, archive, name string) ([]byte, error) {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name != name || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			return readLimited(rc)
		}
		return nil, fmt.Errorf("%s not found in the archive", name)
	}

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s not found in the archive", name)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == name && hdr.Typeflag == tar.TypeReg {
			return readLimited(tr)
		}
	}
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("binary larger than %d bytes", maxDownload)
	}
	return data, nil
}

// replace writes bin next to exe, then swaps it into place.
func replace(exe string, bin []byte) error {
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), ".maisys-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once it's been renamed

	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return swap(tmpName, exe)
}

// RemoveOld deletes binaries an earlier update on Windows moved aside
// (exe.old and exe.old-*). Ones still in use are left for a later run.
func RemoveOld(exe string) {
	matches, _ := filepath.Glob(exe + ".old*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
