package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Asset is a file to download.
type Asset struct {
	// URL is where to download it from.
	URL string
	// Name is its file name, such as "stern_1.35.0_linux_amd64.tar.gz".
	Name string
}

// ChecksumError is a download whose sha256 isn't the one its checksum
// source gives.
type ChecksumError struct {
	// Name is the download's file name.
	Name string
	// Want and Got are the expected and actual sha256, in hex.
	Want, Got string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("checksum mismatch for %s: expected sha256 %s, got %s", e.Name, e.Want, e.Got)
}

// Fetch returns the path of a's file in the download cache, downloading it
// first if the cache doesn't have it. sum is the file's sha256, in hex. A
// download with any other sha256 is deleted and fails with a
// *ChecksumError, and a cached file that no longer matches is downloaded
// again, so the file Fetch returns has always just been verified. Before
// downloading, it deletes the unfinished files earlier downloads of a left
// behind more than a day ago.
func (c *Client) Fetch(ctx context.Context, a Asset, sum string) (string, error) {
	path, sum, err := c.cachePath(a, sum)
	if err != nil {
		return "", err
	}
	got, err := hashFile(path)
	switch {
	case err == nil && got == sum:
		return path, nil
	case err == nil:
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("deleting %s from the download cache: %w", a.Name, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("checking %s in the download cache: %w", a.Name, err)
	}

	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return "", fmt.Errorf("creating the download cache: %w", err)
	}
	removeStaleParts(path)
	err = c.retry(ctx, func() error { return c.download(ctx, a, sum, path) })
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	return path, nil
}

// download makes one attempt at downloading a to path, checking that its
// sha256 is sum.
func (c *Client) download(ctx context.Context, a Asset, sum, path string) (err error) {
	resp, err := c.open(ctx, a.URL, nil, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	f, err := os.CreateTemp(c.Dir, partPrefix(path)+"*"+partSuffix)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		if _, ok := errors.AsType[*fs.PathError](err); ok {
			return err
		}
		return c.readError(resp, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return &ChecksumError{Name: a.Name, Want: sum, Got: got}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// partSuffix ends the name of the file a download is written to until
// it's verified, which partPrefix starts.
const partSuffix = ".part"

// partPrefix returns how the names of the files a download to path is
// written to start: ".<name>.", followed by a random number and
// partSuffix.
func partPrefix(path string) string {
	return "." + filepath.Base(path) + "."
}

// partMaxAge is how old a download's .part file must be to count as left
// behind, as by a maisys killed mid-download: one a download is using is
// written to all the time, or given up.
const partMaxAge = 24 * time.Hour

// removeStaleParts deletes the .part files downloads to path left behind
// more than partMaxAge ago.
func removeStaleParts(path string) {
	dir, prefix := filepath.Dir(path), partPrefix(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.Type().IsRegular() || len(rest) <= len(partSuffix) || !strings.HasSuffix(rest, partSuffix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > partMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Forget deletes a's file from the download cache, if it's there.
func (c *Client) Forget(a Asset, sum string) error {
	path, _, err := c.cachePath(a, sum)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("deleting %s from the download cache: %w", a.Name, err)
	}
	return nil
}

// cachePath returns where a's file is kept in the download cache, by its
// name and checksum, and the checksum in lower case.
func (c *Client) cachePath(a Asset, sum string) (string, string, error) {
	if c.Dir == "" {
		return "", "", errors.New("there's no download cache folder")
	}
	if a.Name == "" || a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, `/\`) {
		return "", "", fmt.Errorf("%q isn't a valid file name for a download", a.Name)
	}
	sum = strings.ToLower(sum)
	if !IsSHA256(sum) {
		return "", "", fmt.Errorf("%q isn't a sha256 checksum", sum)
	}
	return filepath.Join(c.Dir, sum[:16]+"-"+a.Name), sum, nil
}

// IsSHA256 reports whether s is a sha256 checksum in hex.
func IsSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// hashFile returns the sha256 of the file at path, in hex.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
