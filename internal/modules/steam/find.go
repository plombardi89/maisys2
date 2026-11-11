package steam

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/tool"
)

// Where the vendors publish their packages. Tests point them at a local
// server.
var (
	// valveRepo is Valve's apt repo, which steam-launcher adds.
	valveRepo = "https://repo.steampowered.com/steam/"
	// rpmFusion is RPM Fusion's master mirror.
	rpmFusion = "https://download1.rpmfusion.org/"
)

// maxPrimary is the most an uncompressed repodata package list may be.
const maxPrimary = 512 << 20

// findValve finds Valve's steam-launcher .deb for amd64, and its sha256,
// in the Packages index of Valve's apt repo. It's the package
// https://repo.steampowered.com/steam/archive/stable/steam_latest.deb
// holds.
func findValve(ctx context.Context, rt *resource.Runtime) (url, sum string, err error) {
	client, err := clientFor(rt)
	if err != nil {
		return "", "", err
	}
	index := valveRepo + "dists/stable/steam/binary-amd64/Packages"
	data, err := client.Get(ctx, index)
	if err != nil {
		return "", "", fmt.Errorf("reading Valve's package index: %w", err)
	}
	fields, ok := debPackage(data, "steam-launcher")
	if !ok {
		return "", "", fmt.Errorf("the index of Valve's apt repo, %s, has no steam-launcher", index)
	}
	name, sum := fields["Filename"], fields["SHA256"]
	if !fs.ValidPath(name) || !download.IsSHA256(sum) {
		return "", "", fmt.Errorf("the index of Valve's apt repo, %s, doesn't give steam-launcher's file and sha256", index)
	}
	return valveRepo + name, sum, nil
}

// debPackage returns the fields of the first entry for pkg in an apt
// Packages index, and whether there is one.
func debPackage(index []byte, pkg string) (map[string]string, bool) {
	fields := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(index))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			if fields["Package"] == pkg {
				return fields, true
			}
			fields = map[string]string{}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields, fields["Package"] == pkg
}

// findRPMFusion returns how to find RPM Fusion's release package for
// section ("free" or "nonfree") and the machine's Fedora version, and its
// sha256: in the repodata of RPM Fusion's release tree for that version,
// or of its development tree for a Fedora that isn't released yet.
func findRPMFusion(section string) func(ctx context.Context, rt *resource.Runtime) (string, string, error) {
	return func(ctx context.Context, rt *resource.Runtime) (string, string, error) {
		client, err := clientFor(rt)
		if err != nil {
			return "", "", err
		}
		version := rt.Facts.OSVersion
		name := "rpmfusion-" + section + "-release"
		for _, tree := range []string{"releases", "development"} {
			base := fmt.Sprintf("%s%s/fedora/%s/%s/Everything/x86_64/os/", rpmFusion, section, tree, version)
			url, sum, err := findRPM(ctx, client, base, name)
			if download.IsNotFound(err) {
				continue
			}
			return url, sum, err
		}
		return "", "", fmt.Errorf("there's no RPM Fusion %s repo for Fedora %s", section, version)
	}
}

// repomd is the part of a repo's repodata/repomd.xml that says where its
// package list is.
type repomd struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"checksum"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

// rpmPackage is the part of a package in a repo's package list that says
// where it is and what its sha256 is.
type rpmPackage struct {
	Name     string `xml:"name"`
	Checksum struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"checksum"`
	Location struct {
		Href string `xml:"href,attr"`
	} `xml:"location"`
}

// findRPM finds the package name in the dnf repo at base, and returns its
// URL and sha256. A missing repo is an error download.IsNotFound reports.
func findRPM(ctx context.Context, client *download.Client, base, name string) (url, sum string, err error) {
	data, err := client.Get(ctx, base+"repodata/repomd.xml")
	if err != nil {
		return "", "", err
	}
	var md repomd
	if err := xml.Unmarshal(data, &md); err != nil {
		return "", "", fmt.Errorf("reading %srepodata/repomd.xml: %w", base, err)
	}
	var href, want string
	for _, d := range md.Data {
		if d.Type == "primary" && d.Checksum.Type == "sha256" {
			href, want = d.Location.Href, strings.TrimSpace(d.Checksum.Value)
		}
	}
	if !fs.ValidPath(href) || !download.IsSHA256(want) {
		return "", "", fmt.Errorf("%srepodata/repomd.xml doesn't say where the package list is, with its sha256", base)
	}

	data, err = client.Get(ctx, base+href)
	if err != nil {
		return "", "", err
	}
	if got := sha256Of(data); got != strings.ToLower(want) {
		return "", "", fmt.Errorf("the package list %s%s has the sha256 %s, not %s as repomd.xml says", base, href, got, want)
	}
	var list io.Reader = bytes.NewReader(data)
	switch {
	case strings.HasSuffix(href, ".xml.gz"):
		gz, err := gzip.NewReader(list)
		if err != nil {
			return "", "", fmt.Errorf("reading %s%s: %w", base, href, err)
		}
		list = gz
	case strings.HasSuffix(href, ".xml"):
	default:
		return "", "", fmt.Errorf("can't read the package list %s%s: maisys only reads gzip-compressed ones", base, href)
	}

	dec := xml.NewDecoder(io.LimitReader(list, maxPrimary))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return "", "", fmt.Errorf("the repo %s has no %s", base, name)
		}
		if err != nil {
			return "", "", fmt.Errorf("reading %s%s: %w", base, href, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "package" {
			continue
		}
		var p rpmPackage
		if err := dec.DecodeElement(&p, &start); err != nil {
			return "", "", fmt.Errorf("reading %s%s: %w", base, href, err)
		}
		if p.Name != name {
			continue
		}
		sum := strings.TrimSpace(p.Checksum.Value)
		if p.Checksum.Type != "sha256" || !download.IsSHA256(sum) || !fs.ValidPath(p.Location.Href) {
			return "", "", fmt.Errorf("the repo %s doesn't give %s's file and sha256", base, name)
		}
		return base + p.Location.Href, sum, nil
	}
}

// clientFor returns the download client the sync shares.
func clientFor(rt *resource.Runtime) (*download.Client, error) {
	env, err := tool.EnvFor(rt)
	if err != nil {
		return nil, err
	}
	return env.Client, nil
}

// sha256Of returns the sha256 of data, in hex.
func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
