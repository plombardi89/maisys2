package tool

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/version"
)

// upstreamCase is what a tool's spec must say for one version, checked
// against the upstream's real release files.
type upstreamCase struct {
	spec    Spec
	version string
	// amd64 and arm64 are the download URLs on each architecture.
	amd64, arm64 string
	// checksum is where the checksum comes from, on amd64.
	checksum download.Checksum
	// latest is where the latest version comes from.
	latest  string
	archive download.Archive
	links   []Link
}

func upstreamCases() []upstreamCase {
	gh := "https://github.com/"
	return []upstreamCase{
		{
			spec:     Go(),
			version:  "1.27.3",
			amd64:    "https://go.dev/dl/go1.27.3.linux-amd64.tar.gz",
			arm64:    "https://go.dev/dl/go1.27.3.linux-arm64.tar.gz",
			checksum: download.GoIndex{},
			latest:   "https://go.dev/dl/?mode=json",
			archive:  download.Archive{Format: download.TarGz},
			links:    []Link{{"go", "go/bin/go"}, {"gofmt", "go/bin/gofmt"}},
		},
		{
			spec:     Kubectl(),
			version:  "1.37.2",
			amd64:    "https://dl.k8s.io/release/v1.37.2/bin/linux/amd64/kubectl",
			arm64:    "https://dl.k8s.io/release/v1.37.2/bin/linux/arm64/kubectl",
			checksum: download.Sidecar{Suffix: ".sha256"},
			latest:   "https://dl.k8s.io/release/stable.txt",
			archive:  download.Archive{Format: download.Binary, Binary: "kubectl"},
			links:    []Link{{"kubectl", "kubectl"}},
		},
		{
			spec:     Mage(),
			version:  "1.17.2",
			amd64:    gh + "magefile/mage/releases/download/v1.17.2/mage_1.17.2_Linux-64bit.tar.gz",
			arm64:    gh + "magefile/mage/releases/download/v1.17.2/mage_1.17.2_Linux-ARM64.tar.gz",
			checksum: download.SumsFile{URL: gh + "magefile/mage/releases/download/v1.17.2/mage_1.17.2_checksums.txt"},
			latest:   gh + "magefile/mage/releases/latest",
			archive:  download.Archive{Format: download.TarGz},
			links:    []Link{{"mage", "mage"}},
		},
		{
			spec:     GolangciLint(),
			version:  "2.15.0",
			amd64:    gh + "golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz",
			arm64:    gh + "golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-arm64.tar.gz",
			checksum: download.SumsFile{URL: gh + "golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-checksums.txt"},
			latest:   gh + "golangci/golangci-lint/releases/latest",
			archive:  download.Archive{Format: download.TarGz, Strip: 1},
			links:    []Link{{"golangci-lint", "golangci-lint"}},
		},
		{
			spec:     Stern(),
			version:  "1.35.0",
			amd64:    gh + "stern/stern/releases/download/v1.35.0/stern_1.35.0_linux_amd64.tar.gz",
			arm64:    gh + "stern/stern/releases/download/v1.35.0/stern_1.35.0_linux_arm64.tar.gz",
			checksum: download.SumsFile{URL: gh + "stern/stern/releases/download/v1.35.0/checksums.txt"},
			latest:   gh + "stern/stern/releases/latest",
			archive:  download.Archive{Format: download.TarGz},
			links:    []Link{{"stern", "stern"}},
		},
		{
			spec:    Yq(),
			version: "4.54.2",
			amd64:   gh + "mikefarah/yq/releases/download/v4.54.2/yq_linux_amd64",
			arm64:   gh + "mikefarah/yq/releases/download/v4.54.2/yq_linux_arm64",
			checksum: download.ChecksumTable{
				URL:      gh + "mikefarah/yq/releases/download/v4.54.2/checksums",
				OrderURL: gh + "mikefarah/yq/releases/download/v4.54.2/checksums_hashes_order",
			},
			latest:  gh + "mikefarah/yq/releases/latest",
			archive: download.Archive{Format: download.Binary, Binary: "yq"},
			links:   []Link{{"yq", "yq"}},
		},
		{
			spec:     Lnav(),
			version:  "0.14.1",
			amd64:    gh + "tstack/lnav/releases/download/v0.14.1/lnav-0.14.1-linux-musl-x86_64.zip",
			arm64:    gh + "tstack/lnav/releases/download/v0.14.1/lnav-0.14.1-linux-musl-arm64.zip",
			checksum: download.GitHubDigest{Repo: "tstack/lnav", Tag: "v0.14.1"},
			latest:   gh + "tstack/lnav/releases/latest",
			archive:  download.Archive{Format: download.Zip, Strip: 1},
			links:    []Link{{"lnav", "lnav"}},
		},
		{
			spec:     Protoc(),
			version:  "36.2",
			amd64:    gh + "protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-linux-x86_64.zip",
			arm64:    gh + "protocolbuffers/protobuf/releases/download/v36.2/protoc-36.2-linux-aarch_64.zip",
			checksum: download.GitHubDigest{Repo: "protocolbuffers/protobuf", Tag: "v36.2"},
			latest:   gh + "protocolbuffers/protobuf/releases/latest",
			archive:  download.Archive{Format: download.Zip},
			links:    []Link{{"protoc", "bin/protoc"}},
		},
		{
			spec:     Mutagen(),
			version:  "0.18.1",
			amd64:    gh + "mutagen-io/mutagen/releases/download/v0.18.1/mutagen_linux_amd64_v0.18.1.tar.gz",
			arm64:    gh + "mutagen-io/mutagen/releases/download/v0.18.1/mutagen_linux_arm64_v0.18.1.tar.gz",
			checksum: download.SumsFile{URL: gh + "mutagen-io/mutagen/releases/download/v0.18.1/SHA256SUMS"},
			latest:   gh + "mutagen-io/mutagen/releases/latest",
			archive:  download.Archive{Format: download.TarGz},
			links:    []Link{{"mutagen", "mutagen"}},
		},
		{
			spec:     UV(),
			version:  "0.13.7",
			amd64:    gh + "astral-sh/uv/releases/download/0.13.7/uv-x86_64-unknown-linux-gnu.tar.gz",
			arm64:    gh + "astral-sh/uv/releases/download/0.13.7/uv-aarch64-unknown-linux-gnu.tar.gz",
			checksum: download.Sidecar{Suffix: ".sha256"},
			latest:   gh + "astral-sh/uv/releases/latest",
			archive:  download.Archive{Format: download.TarGz, Strip: 1},
			links:    []Link{{"uv", "uv"}, {"uvx", "uvx"}},
		},
		{
			spec:     IntelliJ(),
			version:  "2026.3",
			amd64:    "https://download.jetbrains.com/idea/idea-2026.3.tar.gz",
			arm64:    "https://download.jetbrains.com/idea/idea-2026.3-aarch64.tar.gz",
			checksum: download.Sidecar{Suffix: ".sha256"},
			latest:   "https://data.services.jetbrains.com/products/releases?code=IIU&latest=true&type=release",
			archive:  download.Archive{Format: download.TarGz, Strip: 1},
			links:    []Link{{"idea", "bin/idea"}},
		},
		{
			spec:     IntelliJ(),
			version:  "2025.2.6.4",
			amd64:    "https://download.jetbrains.com/idea/ideaIU-2025.2.6.4.tar.gz",
			arm64:    "https://download.jetbrains.com/idea/ideaIU-2025.2.6.4-aarch64.tar.gz",
			checksum: download.Sidecar{Suffix: ".sha256"},
			latest:   "https://data.services.jetbrains.com/products/releases?code=IIU&latest=true&type=release",
			archive:  download.Archive{Format: download.TarGz, Strip: 1},
			links:    []Link{{"idea", "bin/idea"}},
		},
	}
}

func TestUpstreams(t *testing.T) {
	for _, tt := range upstreamCases() {
		t.Run(tt.spec.Name+" "+tt.version, func(t *testing.T) {
			s := tt.spec
			if err := s.check(); err != nil {
				t.Fatal(err)
			}
			for arch, want := range map[config.Arch]string{config.AMD64: tt.amd64, config.ARM64: tt.arm64} {
				name, ok := s.Arch[arch]
				if !ok {
					t.Fatalf("no download for %s", arch)
				}
				a := s.Asset(tt.version, name)
				if a.URL != want || a.Name != path.Base(want) {
					t.Errorf("on %s, the download is %s (%s), want %s", arch, a.URL, a.Name, want)
				}
				if strings.Contains(a.URL, "amd64") && arch == config.ARM64 || strings.Contains(a.URL, "arm64") && arch == config.AMD64 {
					t.Errorf("on %s, the download is %s, for another architecture", arch, a.URL)
				}
			}
			a := s.Asset(tt.version, s.Arch[config.AMD64])
			if got := s.Checksum(tt.version, a); got != tt.checksum {
				t.Errorf("checksum source = %#v, want %#v", got, tt.checksum)
			}
			if got := s.Latest.Upstream(); got != tt.latest {
				t.Errorf("latest version from %s, want %s", got, tt.latest)
			}
			if s.Archive != tt.archive {
				t.Errorf("archive = %+v, want %+v", s.Archive, tt.archive)
			}
			if fmt.Sprint(s.Links) != fmt.Sprint(tt.links) {
				t.Errorf("commands = %v, want %v", s.Links, tt.links)
			}
		})
	}
}

func TestUpstreamTags(t *testing.T) {
	tests := []struct {
		spec   Spec
		prefix string
	}{
		{Mage(), "v"}, {GolangciLint(), "v"}, {Stern(), "v"}, {Yq(), "v"}, {Lnav(), "v"}, {Protoc(), "v"}, {Mutagen(), "v"}, {UV(), ""},
	}
	for _, tt := range tests {
		if g, ok := tt.spec.Latest.(version.GitHub); !ok || g.TagPrefix != tt.prefix {
			t.Errorf("%s's latest version: %#v, want tags starting with %q", tt.spec.Name, tt.spec.Latest, tt.prefix)
		}
	}
}

func TestIntelliJPrefix(t *testing.T) {
	tests := []struct{ version, want string }{
		{"2024.3.5", "ideaIU-"},
		{"2025.2.6.4", "ideaIU-"},
		{"2025.3", "idea-"},
		{"2025.3.1.1", "idea-"},
		{"2026.1", "idea-"},
		{"2027.1.2", "idea-"},
	}
	for _, tt := range tests {
		if got := intelliJPrefix(tt.version); got != tt.want {
			t.Errorf("intelliJPrefix(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

// rewrite sends every request to a test server instead, with the host it
// was for as the first element of its path.
type rewrite struct {
	to *url.URL
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Path = "/" + req.URL.Host + req.URL.Path
	req.URL.RawPath = ""
	req.URL.Scheme, req.URL.Host, req.Host = r.to.Scheme, r.to.Host, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// TestUpstreamInstalls installs every tool, on both architectures, with
// release files laid out as each upstream's are, from a fake internet.
func TestUpstreamInstalls(t *testing.T) {
	needSymlinks(t)
	for _, tt := range upstreamCases() {
		for _, arch := range config.AllArch() {
			t.Run(tt.spec.Name+" "+tt.version+" "+string(arch), func(t *testing.T) {
				files := &fakeInternet{files: map[string][]byte{}}
				srv := httptest.NewServer(files)
				t.Cleanup(srv.Close)
				to, _ := url.Parse(srv.URL)

				u := newUpstream(t)
				m := newMachine(t, u)
				m.rt.Facts.Arch = arch
				m.env.Client.HTTP = &http.Client{Transport: rewrite{to: to}}

				s := tt.spec
				a := s.Asset(tt.version, s.Arch[arch])
				data := releaseFile(t, s, a.Name)
				files.publish(t, a, data, s.Checksum(tt.version, a))

				tool := New(s, tt.version, m.env)
				wantChange(t, m.sync(tool), resource.Create, s.Name+" "+tt.version)
				for _, l := range s.Links {
					if got, want := m.read(".local/bin/"+l.Name), "the file "+l.Target; got != want {
						t.Errorf("~/.local/bin/%s runs %q, want %q", l.Name, got, want)
					}
				}
				wantChange(t, m.check(New(s, tt.version, m.env)), resource.NoChange, "")
			})
		}
	}
}

// releaseFile returns a release file for spec s, holding its commands' files
// as the upstream's do: in a top-level folder if the spec strips one.
func releaseFile(t *testing.T, s Spec, name string) []byte {
	t.Helper()
	if s.Archive.Format == download.Binary {
		return []byte("the file " + s.Links[0].Target)
	}
	contents := map[string]string{}
	for _, l := range s.Links {
		p := l.Target
		if s.Archive.Strip == 1 {
			p = strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".zip") + "/" + p
		}
		contents[p] = "the file " + l.Target
	}
	if s.Archive.Format == download.Zip {
		return zipOf(t, contents)
	}
	return tarGz(t, contents)
}

// fakeInternet serves files by host and path.
type fakeInternet struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (f *fakeInternet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	f.mu.Lock()
	data, ok := f.files[key]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

// put serves data at rawURL.
func (f *fakeInternet) put(t *testing.T, rawURL string, data []byte) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	key := "/" + u.Host + u.Path
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[key] = data
}

// publish serves download a, holding data, and its checksum as source
// publishes it.
func (f *fakeInternet) publish(t *testing.T, a download.Asset, data []byte, source download.Checksum) {
	t.Helper()
	f.put(t, a.URL, data)
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	switch src := source.(type) {
	case download.GoIndex:
		f.put(t, download.DefaultGoIndex+"?mode=json", marshal(t, []map[string]any{
			{"version": "go1.27.3", "stable": true, "files": []map[string]string{{"filename": a.Name, "sha256": sum}}},
		}))
	case download.Sidecar:
		f.put(t, a.URL+src.Suffix, []byte(sum+" *"+a.Name+"\n"))
	case download.SumsFile:
		f.put(t, src.URL, []byte(strings.Repeat("0", 64)+"  other.tar.gz\n"+sum+"  "+a.Name+"\n"))
	case download.ChecksumTable:
		f.put(t, src.OrderURL, []byte("CRC32\nMD5\nSHA-256\nSHA-512\n"))
		f.put(t, src.URL, []byte(a.Name+"  ee08b91e  710f39f20b3ece085818690f70723658  "+sum+"  "+strings.Repeat("ab", 64)+"\n"))
	case download.GitHubDigest:
		f.put(t, download.DefaultGitHubAPI+"/repos/"+src.Repo+"/releases/tags/"+src.Tag, marshal(t, map[string]any{
			"assets": []map[string]string{{"name": a.Name, "digest": "sha256:" + sum}},
		}))
	default:
		t.Fatalf("no fake for checksum source %T", source)
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
