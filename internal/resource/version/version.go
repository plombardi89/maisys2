// Package version finds the newest stable version of the tools maisys
// installs, from where each upstream publishes it. Every run looks each
// version up once, and a sync uses what the dry run before it found, kept
// for an hour in ~/.cache/maisys/latest.json, so that it installs the
// versions the dry run showed.
package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource/download"
)

// Resolver finds the newest stable version of a tool.
type Resolver interface {
	// Upstream says where the version comes from, such as
	// "https://dl.k8s.io/release/stable.txt". Errors name it, and it keys
	// the cache, so no two resolvers have the same.
	Upstream() string
	// Latest returns the newest stable version.
	Latest(ctx context.Context, c *download.Client) (string, error)
}

// LookupError is a failed lookup of a tool's latest version. A sync stops
// when it gets one: it can't know what to install.
type LookupError struct {
	// Tool is the tool whose version was looked up.
	Tool string
	// Upstream is where it was looked up.
	Upstream string
	// Err is why it failed.
	Err error
}

// StopsSync reports that a failed lookup stops the whole sync.
func (e *LookupError) StopsSync() bool { return true }

func (e *LookupError) Error() string {
	// HTTP errors start with the URL, which is often the upstream itself.
	msg := strings.TrimPrefix(e.Err.Error(), e.Upstream+": ")
	return fmt.Sprintf("can't find the latest version of %s from %s: %s (pin a version to stop depending on it)", e.Tool, e.Upstream, msg)
}

func (e *LookupError) Unwrap() error { return e.Err }

// newLookupError returns the LookupError for a failed lookup of tool's
// latest version from upstream, or from the upstream err names, if it
// asked somewhere else.
func newLookupError(tool, upstream string, err error) *LookupError {
	if ue, ok := errors.AsType[*upstreamError](err); ok {
		upstream = ue.upstream
	}
	return &LookupError{Tool: tool, Upstream: upstream, Err: err}
}

// upstreamError is a failed lookup that asked somewhere other than its
// resolver's Upstream, such as go list for a Go module fetched directly.
type upstreamError struct {
	upstream string
	err      error
}

func (e *upstreamError) Error() string { return e.err.Error() }

func (e *upstreamError) Unwrap() error { return e.err }

// checkVersion returns v if it's a well-formed exact version, which is safe
// to put in file names and URLs.
func checkVersion(v string) (string, error) {
	if !config.IsExactVersion(v) {
		return "", fmt.Errorf("%q isn't a version", v)
	}
	return v, nil
}

// DefaultGoIndex is the Go download index, which lists the current
// releases first.
const DefaultGoIndex = "https://go.dev/dl/?mode=json"

// Go is the Go toolchain's latest version: the first stable release the Go
// download index lists. Versions have no "go" prefix, such as "1.27.3".
type Go struct {
	// URL is the index's URL. Empty means DefaultGoIndex.
	URL string
}

// Upstream returns the index's URL.
func (g Go) Upstream() string {
	if g.URL == "" {
		return DefaultGoIndex
	}
	return g.URL
}

// Latest returns the first stable release in the index.
func (g Go) Latest(ctx context.Context, c *download.Client) (string, error) {
	data, err := c.Get(ctx, g.Upstream())
	if err != nil {
		return "", err
	}
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.Unmarshal(data, &releases); err != nil {
		return "", fmt.Errorf("reading the index: %w", err)
	}
	for _, r := range releases {
		if r.Stable {
			return checkVersion(strings.TrimPrefix(r.Version, "go"))
		}
	}
	return "", errors.New("the index lists no stable release")
}

// DefaultKubernetesStable is where Kubernetes publishes its latest stable
// release.
const DefaultKubernetesStable = "https://dl.k8s.io/release/stable.txt"

// Text is a text file holding the latest version, such as Kubernetes'
// stable.txt. A leading "v" is dropped.
type Text struct {
	// URL is the file's URL.
	URL string
}

// Upstream returns the file's URL.
func (t Text) Upstream() string { return t.URL }

// Latest returns the version in the file.
func (t Text) Latest(ctx context.Context, c *download.Client) (string, error) {
	data, err := c.Get(ctx, t.URL)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	return checkVersion(strings.TrimPrefix(strings.TrimSpace(line), "v"))
}

// DefaultGitHub is GitHub's web address.
const DefaultGitHub = "https://github.com"

// GitHub is the latest release of a GitHub repo, from where the repo's
// releases/latest page redirects: it skips drafts and pre-releases, and
// isn't limited by the API's request quota.
type GitHub struct {
	// Repo is the repo, as owner/name.
	Repo string
	// TagPrefix is the text before the version in the repo's tags, such
	// as "v". It's dropped from the version.
	TagPrefix string
	// URL is GitHub's web address. Empty means DefaultGitHub.
	URL string
}

// Upstream returns the URL of the repo's latest release.
func (g GitHub) Upstream() string {
	base := g.URL
	if base == "" {
		base = DefaultGitHub
	}
	return strings.TrimSuffix(base, "/") + "/" + g.Repo + "/releases/latest"
}

// Latest returns the version in the latest release's tag.
func (g GitHub) Latest(ctx context.Context, c *download.Client) (string, error) {
	loc, err := c.Redirect(ctx, g.Upstream())
	if download.IsNotFound(err) {
		return "", fmt.Errorf("there's no public GitHub repo %s", g.Repo)
	}
	if err != nil {
		return "", err
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	_, tag, ok := strings.Cut(u.EscapedPath(), "/releases/tag/")
	if !ok || tag == "" {
		return "", fmt.Errorf("%s has no published releases", g.Repo)
	}
	if tag, err = url.PathUnescape(tag); err != nil {
		return "", err
	}
	v, ok := strings.CutPrefix(tag, g.TagPrefix)
	if !ok {
		return "", fmt.Errorf("the latest release's tag, %s, doesn't start with %q", tag, g.TagPrefix)
	}
	return checkVersion(v)
}

// DefaultJetBrains is JetBrains' release service.
const DefaultJetBrains = "https://data.services.jetbrains.com/products/releases"

// JetBrains is the latest release of a JetBrains product, from JetBrains'
// release service.
type JetBrains struct {
	// Code is the product's code, such as "IIU" for IntelliJ IDEA
	// Ultimate.
	Code string
	// URL is the release service's URL, without a query. Empty means
	// DefaultJetBrains.
	URL string
}

// Upstream returns the URL that asks for the product's latest release.
func (j JetBrains) Upstream() string {
	base := j.URL
	if base == "" {
		base = DefaultJetBrains
	}
	return base + "?code=" + url.QueryEscape(j.Code) + "&latest=true&type=release"
}

// Latest returns the version of the product's latest release.
func (j JetBrains) Latest(ctx context.Context, c *download.Client) (string, error) {
	data, err := c.Get(ctx, j.Upstream())
	if err != nil {
		return "", err
	}
	var products map[string][]struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &products); err != nil {
		return "", fmt.Errorf("reading JetBrains' releases: %w", err)
	}
	releases := products[j.Code]
	if len(releases) == 0 {
		return "", fmt.Errorf("JetBrains lists no release of %s", j.Code)
	}
	return checkVersion(releases[0].Version)
}
