package download

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/plombardi89/maisys2/internal/resource/cachefile"
)

// Checksum is where a download's sha256 comes from.
type Checksum interface {
	// SHA256 returns the sha256 of the file a, in lower-case hex. If the
	// source has none for it, the error is a *NoChecksumError.
	SHA256(ctx context.Context, c *Client, a Asset) (string, error)
}

// NoChecksumError means a download has no checksum, so it can't be
// verified, and isn't used.
type NoChecksumError struct {
	// Name is the download's file name.
	Name string
	// Reason says why there's no checksum, such as "its release has no
	// checksums file and predates GitHub's asset digests (June 2025); pin
	// a newer version".
	Reason string
}

func (e *NoChecksumError) Error() string {
	return fmt.Sprintf("%s has no checksum: %s", e.Name, e.Reason)
}

// Sidecar is a checksum file published next to each download, at the
// download's URL plus Suffix, such as kubectl's, uv's and JetBrains' ".sha256"
// files. It holds the sha256, optionally followed by the file's name.
type Sidecar struct {
	// Suffix is added to the download's URL, such as ".sha256".
	Suffix string
}

// SHA256 returns the sha256 in a's checksum file.
func (s Sidecar) SHA256(ctx context.Context, c *Client, a Asset) (string, error) {
	data, err := c.Get(ctx, a.URL+s.Suffix)
	if err != nil {
		return "", err
	}
	name := path.Base(a.URL) + s.Suffix
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("%s is empty", name)
	}
	sum := strings.ToLower(fields[0])
	if !IsSHA256(sum) {
		return "", fmt.Errorf("%s doesn't hold a sha256 checksum", name)
	}
	if len(fields) > 1 {
		if listed := strings.TrimPrefix(fields[1], "*"); listed != a.Name {
			return "", fmt.Errorf("%s is the checksum of %s, not %s", name, listed, a.Name)
		}
	}
	return sum, nil
}

// SumsFile is a file listing the sha256 of each of a release's files, in
// sha256sum's format ("<sha256>  <name>"), such as a GitHub release's
// checksums.txt.
type SumsFile struct {
	// URL is the file's URL.
	URL string
}

// SHA256 returns the sha256 the file lists for a.
func (s SumsFile) SHA256(ctx context.Context, c *Client, a Asset) (string, error) {
	data, err := c.Get(ctx, s.URL)
	if err != nil {
		return "", err
	}
	file := path.Base(s.URL)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != a.Name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if !IsSHA256(sum) {
			return "", fmt.Errorf("%s lists %s with %q, which isn't a sha256 checksum", file, a.Name, fields[0])
		}
		return sum, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	return "", &NoChecksumError{Name: a.Name, Reason: file + " doesn't list it"}
}

// ChecksumTable is yq's checksums file: a line for each of a release's
// files, with its name followed by many checksums, in the order another
// file, OrderURL, lists their algorithms (one of which is "SHA-256").
type ChecksumTable struct {
	// URL is the checksums file's URL.
	URL string
	// OrderURL is the URL of the file naming the algorithms, a line each.
	OrderURL string
}

// SHA256 returns the sha256 the table lists for a.
func (t ChecksumTable) SHA256(ctx context.Context, c *Client, a Asset) (string, error) {
	order, err := c.Get(ctx, t.OrderURL)
	if err != nil {
		return "", err
	}
	column := -1
	for i, alg := range strings.Fields(string(order)) {
		if alg == "SHA-256" {
			column = i + 1
			break
		}
	}
	if column < 0 {
		return "", &NoChecksumError{Name: a.Name, Reason: path.Base(t.OrderURL) + " has no SHA-256 column"}
	}

	data, err := c.Get(ctx, t.URL)
	if err != nil {
		return "", err
	}
	file := path.Base(t.URL)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != a.Name {
			continue
		}
		if column >= len(fields) || !IsSHA256(strings.ToLower(fields[column])) {
			return "", fmt.Errorf("%s has no sha256 checksum for %s in the column %s gives", file, a.Name, path.Base(t.OrderURL))
		}
		return strings.ToLower(fields[column]), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	return "", &NoChecksumError{Name: a.Name, Reason: file + " doesn't list it"}
}

// DefaultGoIndex is the Go download index's URL.
const DefaultGoIndex = "https://go.dev/dl/"

// GoIndex is the Go download index, which lists the sha256 of every file
// of every Go release.
type GoIndex struct {
	// URL is the index's URL, without a query. Empty means DefaultGoIndex.
	URL string
}

// SHA256 returns the sha256 the index lists for a. It looks in the index of
// current releases first, then in the one of every release.
func (g GoIndex) SHA256(ctx context.Context, c *Client, a Asset) (string, error) {
	base := g.URL
	if base == "" {
		base = DefaultGoIndex
	}
	for _, query := range []string{"?mode=json", "?mode=json&include=all"} {
		data, err := c.Get(ctx, base+query)
		if err != nil {
			return "", err
		}
		var releases []struct {
			Files []struct {
				Filename string `json:"filename"`
				SHA256   string `json:"sha256"`
			} `json:"files"`
		}
		if err := json.Unmarshal(data, &releases); err != nil {
			return "", fmt.Errorf("reading the Go download index: %w", err)
		}
		for _, r := range releases {
			for _, f := range r.Files {
				if f.Filename != a.Name {
					continue
				}
				sum := strings.ToLower(f.SHA256)
				if !IsSHA256(sum) {
					return "", fmt.Errorf("the Go download index lists %s with %q, which isn't a sha256 checksum", a.Name, f.SHA256)
				}
				return sum, nil
			}
		}
	}
	return "", &NoChecksumError{Name: a.Name, Reason: "the Go download index doesn't list it"}
}

// DefaultGitHubAPI is the base URL of GitHub's REST API.
const DefaultGitHubAPI = "https://api.github.com"

// GitHubDigest is the sha256 GitHub publishes for each file of a release
// (its "digest"), which it has done for files uploaded since June 2025.
// It's for GitHub-released tools without a checksums file of their own.
// Digests never change, so the Client's Digests keep them for good.
type GitHubDigest struct {
	// Repo is the GitHub repo, as owner/name.
	Repo string
	// Tag is the release's tag.
	Tag string
	// API is the base URL of GitHub's REST API. Empty means
	// DefaultGitHubAPI.
	API string
}

// noDigest is why a GitHub release file has no digest.
const noDigest = "its release has no checksums file and predates GitHub's asset digests (June 2025); pin a newer version"

// SHA256 returns GitHub's digest of a, from the cache if it has it.
func (g GitHubDigest) SHA256(ctx context.Context, c *Client, a Asset) (string, error) {
	key := g.Repo + "/" + g.Tag + "/" + a.Name
	if c.Digests != nil {
		sum, ok, err := c.Digests.get(key)
		if err != nil || ok {
			return sum, err
		}
	}

	api := g.API
	if api == "" {
		api = DefaultGitHubAPI
	}
	header := http.Header{}
	header.Set("Accept", "application/vnd.github+json")
	header.Set("X-Github-Api-Version", "2022-11-28")
	data, err := c.get(ctx, strings.TrimSuffix(api, "/")+"/repos/"+g.Repo+"/releases/tags/"+url.PathEscape(g.Tag), header)
	if IsNotFound(err) {
		return "", fmt.Errorf("%s has no release %s on GitHub", g.Repo, g.Tag)
	}
	if err != nil {
		return "", err
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return "", fmt.Errorf("reading GitHub's release %s of %s: %w", g.Tag, g.Repo, err)
	}

	found := map[string]string{}
	listed := false
	for _, asset := range release.Assets {
		listed = listed || asset.Name == a.Name
		if sum, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && IsSHA256(strings.ToLower(sum)) {
			found[g.Repo+"/"+g.Tag+"/"+asset.Name] = strings.ToLower(sum)
		}
	}
	if c.Digests != nil && len(found) > 0 {
		if err := c.Digests.put(found); err != nil {
			return "", err
		}
	}
	switch sum, ok := found[key]; {
	case ok:
		return sum, nil
	case !listed:
		return "", fmt.Errorf("release %s of %s has no file %s", g.Tag, g.Repo, a.Name)
	default:
		return "", &NoChecksumError{Name: a.Name, Reason: noDigest}
	}
}

// DigestsFormatVersion is the format of digests.json this maisys reads and
// writes.
const DigestsFormatVersion = 1

// Digests caches the digests GitHub publishes for release files, in
// ~/.cache/maisys/digests.json, keyed by owner/repo/tag/file. They never
// change, so they're kept for good, which keeps maisys well inside the 60
// requests an hour GitHub's API allows without signing in. It's safe for
// concurrent use.
type Digests struct {
	// Path is the cache file.
	Path string

	mu      sync.Mutex
	loaded  bool
	digests map[string]string
}

// digestsFile is digests.json's layout.
type digestsFile struct {
	FormatVersion int               `json:"format_version"`
	Digests       map[string]string `json:"digests"`
}

// Load reads the cache file, unless it's been read already. Only a file
// from a newer maisys is an error: one that can't be read is replaced.
func (d *Digests) Load() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loadLocked()
}

func (d *Digests) loadLocked() error {
	if d.loaded {
		return nil
	}
	var f digestsFile
	ok, err := cachefile.Load(d.Path, DigestsFormatVersion, &f)
	if err != nil {
		return err
	}
	d.digests = map[string]string{}
	if ok {
		for k, v := range f.Digests {
			if IsSHA256(v) {
				d.digests[k] = v
			}
		}
	}
	d.loaded = true
	return nil
}

func (d *Digests) get(key string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.loadLocked(); err != nil {
		return "", false, err
	}
	sum, ok := d.digests[key]
	return sum, ok, nil
}

// put adds digests to the cache, and saves it.
func (d *Digests) put(digests map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.loadLocked(); err != nil {
		return err
	}
	maps.Copy(d.digests, digests)
	if err := cachefile.Save(d.Path, digestsFile{FormatVersion: DigestsFormatVersion, Digests: d.digests}); err != nil {
		return fmt.Errorf("caching GitHub's digests: %w", err)
	}
	return nil
}
