package steam

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/repo"
	"github.com/plombardi89/maisys2/internal/resource/tool"
)

func TestResources(t *testing.T) {
	tests := []struct {
		name    string
		system  facts.System
		want    []string
		wantErr string
	}{
		{
			name:   "ubuntu",
			system: facts.System{OS: config.Ubuntu, OSCodename: "noble", Arch: config.AMD64, RawArch: "amd64"},
			want: []string{
				"dpkg-arch:i386", "repo:steam",
				"package:steam/steam-launcher", "package:steam/steam-libs-amd64", "package:steam/steam-libs-i386:i386",
			},
		},
		{
			name:   "fedora",
			system: facts.System{OS: config.Fedora, OSVersion: "44", Arch: config.AMD64, RawArch: "amd64"},
			want:   []string{"repo:rpmfusion-free", "repo:rpmfusion-nonfree", "package:steam/steam"},
		},
		{
			name:    "arm64",
			system:  facts.System{OS: config.Fedora, OSVersion: "44", Arch: config.ARM64, RawArch: "arm64"},
			wantErr: "steam only works on amd64, because Valve has no Steam client for arm64",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := Module{}.Resources(&Config{Enabled: true}, &resource.Runtime{Facts: &tt.system})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Resources() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
				if rp, ok := r.(*repo.ReleasePackage); ok && rp.Find == nil {
					t.Errorf("%s has no way to find its package", r.ID())
				}
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("resources = %q, want %q", ids, tt.want)
			}
		})
	}
}

// serve serves files by path, and points the vendors' URLs at them.
func serve(t *testing.T, files map[string][]byte) *resource.Runtime {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	oldValve, oldFusion := valveRepo, rpmFusion
	valveRepo, rpmFusion = srv.URL+"/steam/", srv.URL+"/"
	t.Cleanup(func() { valveRepo, rpmFusion = oldValve, oldFusion })
	rt := &resource.Runtime{Facts: &facts.System{OS: config.Fedora, OSVersion: "44", Arch: config.AMD64}}
	rt.SetShared(tool.EnvKey{}, &tool.Env{Client: &download.Client{HTTP: srv.Client(), Attempts: 1}})
	return rt
}

const launcherSum = "765aba9a0ed339a50226ceb614fcc9879a991ba184098bc8de920efb12c714a4"

// valveIndex is part of Valve's Packages index.
const valveIndex = `Package: steam-libs-amd64
Version: 1:1.0.0.87
Architecture: amd64
Filename: pool/steam/s/steam/steam-libs-amd64_1.0.0.87_amd64.deb
SHA256: 89a19f70808757c236376ec4bac7748b90a11efbe26def951c81d2b37079c56d

Package: steam-launcher
Source: steam
Version: 1:1.0.0.87
Architecture: amd64
Depends: apt (>= 1.1), apt (>= 1.6) | apt-transport-https,
 ca-certificates
Filename: pool/steam/s/steam/steam-launcher_1.0.0.87_amd64.deb
Size: 20379390
SHA256: ` + launcherSum + `
Description: Launcher for the Steam software distribution service
 Steam is a software distribution service.
`

func TestFindValve(t *testing.T) {
	tests := []struct {
		name    string
		index   string
		wantURL string
		wantErr string
	}{
		{name: "found", index: valveIndex, wantURL: "/steam/pool/steam/s/steam/steam-launcher_1.0.0.87_amd64.deb"},
		{name: "found last, without a blank line", index: strings.TrimSuffix(valveIndex, "\n"), wantURL: "/steam/pool/steam/s/steam/steam-launcher_1.0.0.87_amd64.deb"},
		{name: "missing", index: "Package: steam-libs-amd64\nSHA256: " + launcherSum + "\n", wantErr: "has no steam-launcher"},
		{name: "no sha256", index: "Package: steam-launcher\nFilename: pool/steam-launcher.deb\n", wantErr: "doesn't give steam-launcher's file and sha256"},
		{name: "a file outside the repo", index: "Package: steam-launcher\nFilename: ../steam-launcher.deb\nSHA256: " + launcherSum + "\n", wantErr: "doesn't give steam-launcher's file and sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := serve(t, map[string][]byte{"/steam/dists/stable/steam/binary-amd64/Packages": []byte(tt.index)})
			url, sum, err := findValve(t.Context(), rt)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("findValve() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(url, tt.wantURL) || !strings.HasPrefix(url, valveRepo) || sum != launcherSum {
				t.Errorf("findValve() = %q, %q, want ...%s and %s", url, sum, tt.wantURL, launcherSum)
			}
		})
	}
}

func gz(t *testing.T, data string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const releaseSum = "178e9e0a2a263fda8f010ab830af728d4d48a70ef260852c0e38c76561687787"

// primary is part of a repo's package list.
const primary = `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="2">
<package type="rpm">
  <name>rpmfusion-free-appstream-data</name>
  <arch>noarch</arch>
  <checksum type="sha256" pkgid="YES">0000000000000000000000000000000000000000000000000000000000000000</checksum>
  <location href="Packages/r/rpmfusion-free-appstream-data-44-1.noarch.rpm"/>
</package>
<package type="rpm">
  <name>rpmfusion-free-release</name>
  <arch>noarch</arch>
  <version epoch="0" ver="44" rel="1"/>
  <checksum type="sha256" pkgid="YES">` + releaseSum + `</checksum>
  <location href="Packages/r/rpmfusion-free-release-44-1.noarch.rpm"/>
  <format>
    <rpm:license>BSD</rpm:license>
    <rpm:provides>
      <rpm:entry name="rpmfusion-free-release" flags="EQ" epoch="0" ver="44" rel="1"/>
    </rpm:provides>
  </format>
</package>
</metadata>
`

// repomdFor returns a repomd.xml for a package list at href with data.
func repomdFor(href string, data []byte) []byte {
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
<data type="filelists">
  <checksum type="sha256">%[1]s</checksum>
  <location href="repodata/filelists.xml.gz"/>
</data>
<data type="primary">
  <checksum type="sha256">%[1]s</checksum>
  <location href="%[2]s"/>
</data>
</repomd>
`, sha256Of(data), href)
}

func TestFindRPMFusion(t *testing.T) {
	list := gz(t, primary)
	const href = "repodata/abc-primary.xml.gz"
	tests := []struct {
		name    string
		files   map[string][]byte
		wantURL string
		wantErr string
	}{
		{
			name: "released",
			files: map[string][]byte{
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/repomd.xml": repomdFor(href, list),
				"/free/fedora/releases/44/Everything/x86_64/os/" + href:             list,
			},
			wantURL: "/free/fedora/releases/44/Everything/x86_64/os/Packages/r/rpmfusion-free-release-44-1.noarch.rpm",
		},
		{
			name: "not released yet",
			files: map[string][]byte{
				"/free/fedora/development/44/Everything/x86_64/os/repodata/repomd.xml": repomdFor(href, list),
				"/free/fedora/development/44/Everything/x86_64/os/" + href:             list,
			},
			wantURL: "/free/fedora/development/44/Everything/x86_64/os/Packages/r/rpmfusion-free-release-44-1.noarch.rpm",
		},
		{
			name:    "no repo",
			files:   map[string][]byte{},
			wantErr: "there's no RPM Fusion free repo for Fedora 44",
		},
		{
			name: "a package list with the wrong sha256",
			files: map[string][]byte{
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/repomd.xml": repomdFor(href, []byte("other")),
				"/free/fedora/releases/44/Everything/x86_64/os/" + href:             list,
			},
			wantErr: "as repomd.xml says",
		},
		{
			name: "a package list maisys can't read",
			files: map[string][]byte{
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/repomd.xml":          repomdFor("repodata/abc-primary.xml.zst", list),
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/abc-primary.xml.zst": list,
			},
			wantErr: "maisys only reads gzip-compressed ones",
		},
		{
			name: "no release package",
			files: map[string][]byte{
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/repomd.xml":  repomdFor("repodata/primary.xml", []byte("<metadata></metadata>")),
				"/free/fedora/releases/44/Everything/x86_64/os/repodata/primary.xml": []byte("<metadata></metadata>"),
			},
			wantErr: "has no rpmfusion-free-release",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := serve(t, tt.files)
			url, sum, err := findRPMFusion("free")(t.Context(), rt)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if url != strings.TrimSuffix(rpmFusion, "/")+tt.wantURL || sum != releaseSum {
				t.Errorf("found %q, %q, want ...%s and %s", url, sum, tt.wantURL, releaseSum)
			}
		})
	}
}
