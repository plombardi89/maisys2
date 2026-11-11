package download

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sumA = "03729b01a8fcfab25f6ea53389bd38bc37400ff86d90cd2dcda541f248cf843e"
	sumB = "b12f5e2ee0d01f6f21e0759cced0be2d8df9cd4e93e6a3cf11393170ae0f43cf"
)

func TestSidecar(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr string
	}{
		{name: "kubectl: just the checksum", content: sumA, want: sumA},
		{name: "JetBrains: binary mode", content: sumA + " *idea-2026.3.tar.gz\n", want: sumA},
		{name: "uv: text mode", content: strings.ToUpper(sumA) + "  idea-2026.3.tar.gz\n", want: sumA},
		{name: "another file's", content: sumA + "  idea-2026.2.tar.gz\n", wantErr: "idea-2026.3.tar.gz.sha256 is the checksum of idea-2026.2.tar.gz, not idea-2026.3.tar.gz"},
		{name: "empty", content: "\n", wantErr: "idea-2026.3.tar.gz.sha256 is empty"},
		{name: "not a checksum", content: "<html>", wantErr: "doesn't hold a sha256 checksum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.file("/idea/idea-2026.3.tar.gz.sha256", []byte(tt.content))
			c := newClient(t, s, nil)
			a := Asset{URL: s.URL + "/idea/idea-2026.3.tar.gz", Name: "idea-2026.3.tar.gz"}
			got, err := Sidecar{Suffix: ".sha256"}.SHA256(t.Context(), c, a)
			check(t, got, err, tt.want, tt.wantErr)
		})
	}

	s := newServer(t)
	c := newClient(t, s, nil)
	_, err := Sidecar{Suffix: ".sha256"}.SHA256(t.Context(), c, Asset{URL: s.URL + "/kubectl", Name: "kubectl"})
	if !IsNotFound(err) {
		t.Errorf("SHA256() without a checksum file: error = %v, want a not found error", err)
	}
}

// check compares a checksum source's result with want, or its error with
// wantErr.
func check(t *testing.T, got string, err error, want, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("SHA256() = %q, %v, want an error containing %q", got, err, wantErr)
		}
		return
	}
	if err != nil || got != want {
		t.Errorf("SHA256() = %q, %v, want %q", got, err, want)
	}
}

func TestSumsFile(t *testing.T) {
	sums := sumB + "  stern_1.35.0_linux_arm.tar.gz\n" +
		sumA + "  stern_1.35.0_linux_amd64.tar.gz\n" +
		strings.ToUpper(sumB) + " *stern_1.35.0_linux_arm64.tar.gz\n" +
		"nonsense  stern_1.35.0_linux_s390x.tar.gz\n"
	tests := []struct {
		name, asset, want, wantErr string
	}{
		{name: "listed", asset: "stern_1.35.0_linux_amd64.tar.gz", want: sumA},
		{name: "binary mode", asset: "stern_1.35.0_linux_arm64.tar.gz", want: sumB},
		{name: "not listed", asset: "stern_1.35.0_linux_riscv64.tar.gz", wantErr: "stern_1.35.0_linux_riscv64.tar.gz has no checksum: checksums.txt doesn't list it"},
		{name: "invalid", asset: "stern_1.35.0_linux_s390x.tar.gz", wantErr: `checksums.txt lists stern_1.35.0_linux_s390x.tar.gz with "nonsense"`},
	}
	s := newServer(t)
	s.file("/v1.35.0/checksums.txt", []byte(sums))
	c := newClient(t, s, nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SumsFile{URL: s.URL + "/v1.35.0/checksums.txt"}.SHA256(t.Context(), c, Asset{Name: tt.asset})
			check(t, got, err, tt.want, tt.wantErr)
		})
	}

	_, err := SumsFile{URL: s.URL + "/v1.35.0/checksums.txt"}.SHA256(t.Context(), c, Asset{Name: "stern_1.35.0_linux_riscv64.tar.gz"})
	if _, ok := errors.AsType[*NoChecksumError](err); !ok {
		t.Errorf("SHA256() of a file that isn't listed: error = %v, want a *NoChecksumError", err)
	}
}

func TestChecksumTable(t *testing.T) {
	// yq's checksums file lists many hashes for each file, in the order
	// checksums_hashes_order gives.
	order := "CRC32\nMD4\nMD5\nSHA1\nSHA-224\nSHA-256\nSHA-384\n"
	table := "yq_linux_amd64  ee08b91e  710f39f20b3ece085818690f70723658  9aa31213488bdb34493dd7bf097782de  13a9d600f4764bdb18000a1ccba849c779474b6c  2748514c4047eb9c99474101de0fa2fdbb0a547c5e210aa65587ee9c  " + sumA + "  2eaa02f7024275f2b21e282219489c99e98c631a499edeec14936eff74a2171e24b3b016a7182c8a646e831a587419b7\n" +
		"yq_linux_amd64.tar.gz  ee08b91e  710f39f20b3ece085818690f70723658  9aa31213488bdb34493dd7bf097782de  13a9d600f4764bdb18000a1ccba849c779474b6c  2748514c4047eb9c99474101de0fa2fdbb0a547c5e210aa65587ee9c  " + sumB + "  2eaa02f7\n" +
		"yq_linux_short  ee08b91e\n"
	tests := []struct {
		name, asset, order, want, wantErr string
	}{
		{name: "binary", asset: "yq_linux_amd64", order: order, want: sumA},
		{name: "archive", asset: "yq_linux_amd64.tar.gz", order: order, want: sumB},
		{name: "not listed", asset: "yq_linux_arm64", order: order, wantErr: "yq_linux_arm64 has no checksum: checksums doesn't list it"},
		{name: "short line", asset: "yq_linux_short", order: order, wantErr: "checksums has no sha256 checksum for yq_linux_short"},
		{name: "no SHA-256", asset: "yq_linux_amd64", order: "CRC32\nMD5\n", wantErr: "checksums_hashes_order has no SHA-256 column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.file("/checksums", []byte(table))
			s.file("/checksums_hashes_order", []byte(tt.order))
			c := newClient(t, s, nil)
			got, err := ChecksumTable{URL: s.URL + "/checksums", OrderURL: s.URL + "/checksums_hashes_order"}.SHA256(t.Context(), c, Asset{Name: tt.asset})
			check(t, got, err, tt.want, tt.wantErr)
		})
	}
}

// goIndex returns a Go download index listing the given files of each
// version.
func goIndex(t *testing.T, files map[string]map[string]string) []byte {
	t.Helper()
	type file struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		SHA256   string `json:"sha256"`
	}
	type release struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []file `json:"files"`
	}
	var index []release
	for v, fs := range files {
		r := release{Version: v, Stable: true}
		for name, sum := range fs {
			r.Files = append(r.Files, file{Filename: name, OS: "linux", Arch: "amd64", SHA256: sum})
		}
		index = append(index, r)
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoIndex(t *testing.T) {
	s := newServer(t)
	s.file("/dl/?mode=json", goIndex(t, map[string]map[string]string{
		"go1.27.3": {"go1.27.3.linux-amd64.tar.gz": sumA, "go1.27.3.linux-arm64.tar.gz": sumB},
	}))
	s.file("/dl/?mode=json&include=all", goIndex(t, map[string]map[string]string{
		"go1.27.3": {"go1.27.3.linux-amd64.tar.gz": sumA},
		"go1.26.4": {"go1.26.4.linux-amd64.tar.gz": sumB},
	}))
	c := newClient(t, s, nil)
	index := GoIndex{URL: s.URL + "/dl/"}

	got, err := index.SHA256(t.Context(), c, Asset{Name: "go1.27.3.linux-arm64.tar.gz"})
	check(t, got, err, sumB, "")
	if n := s.count("/dl/?mode=json&include=all"); n != 0 {
		t.Errorf("SHA256() of a current release read the index of every release")
	}

	got, err = index.SHA256(t.Context(), c, Asset{Name: "go1.26.4.linux-amd64.tar.gz"})
	check(t, got, err, sumB, "")

	got, err = index.SHA256(t.Context(), c, Asset{Name: "go1.19.linux-amd64.tar.gz"})
	check(t, got, err, "", "go1.19.linux-amd64.tar.gz has no checksum: the Go download index doesn't list it")
}

// githubRelease returns GitHub's API answer for a release with the given
// files and digests ("" for none).
func githubRelease(t *testing.T, assets map[string]string) []byte {
	t.Helper()
	type asset struct {
		Name   string  `json:"name"`
		Digest *string `json:"digest"`
	}
	var list []asset
	for name, digest := range assets {
		a := asset{Name: name}
		if digest != "" {
			a.Digest = &digest
		}
		list = append(list, a)
	}
	data, err := json.Marshal(map[string]any{"tag_name": "v36.2", "assets": list})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGitHubDigest(t *testing.T) {
	s := newServer(t)
	var accept []string
	s.handle("/repos/protocolbuffers/protobuf/releases/tags/v36.2", func(w http.ResponseWriter, r *http.Request) {
		accept = append(accept, r.Header.Get("Accept"))
		_, _ = w.Write(githubRelease(t, map[string]string{
			"protoc-36.2-linux-x86_64.zip":   "sha256:" + sumA,
			"protoc-36.2-linux-aarch_64.zip": "sha256:" + sumB,
		}))
	})
	s.file("/repos/tstack/lnav/releases/tags/v0.12.4", githubRelease(t, map[string]string{
		"lnav-0.12.4-linux-musl-x86_64.zip": "",
	}))
	cacheFile := filepath.Join(t.TempDir(), "digests.json")
	c := newClient(t, s, nil)
	c.Digests = &Digests{Path: cacheFile}
	protoc := GitHubDigest{Repo: "protocolbuffers/protobuf", Tag: "v36.2", API: s.URL}

	got, err := protoc.SHA256(t.Context(), c, Asset{Name: "protoc-36.2-linux-x86_64.zip"})
	check(t, got, err, sumA, "")
	if len(accept) != 1 || accept[0] != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", accept)
	}
	// Every digest of the release is cached, and kept for good.
	got, err = protoc.SHA256(t.Context(), c, Asset{Name: "protoc-36.2-linux-aarch_64.zip"})
	check(t, got, err, sumB, "")
	c.Digests = &Digests{Path: cacheFile}
	got, err = protoc.SHA256(t.Context(), c, Asset{Name: "protoc-36.2-linux-x86_64.zip"})
	check(t, got, err, sumA, "")
	if n := s.count("/repos/protocolbuffers/protobuf/releases/tags/v36.2"); n != 1 {
		t.Errorf("SHA256() asked GitHub %d times, want once", n)
	}
	var f struct {
		FormatVersion int               `json:"format_version"`
		Digests       map[string]string `json:"digests"`
	}
	data, _ := os.ReadFile(cacheFile)
	if err := json.Unmarshal(data, &f); err != nil || f.FormatVersion != 1 || f.Digests["protocolbuffers/protobuf/v36.2/protoc-36.2-linux-aarch_64.zip"] != sumB {
		t.Errorf("digests.json = %s, want format_version 1 and the digests by owner/repo/tag/file", data)
	}

	tests := []struct {
		name    string
		digest  GitHubDigest
		asset   string
		wantErr string
	}{
		{
			name:    "before GitHub's digests",
			digest:  GitHubDigest{Repo: "tstack/lnav", Tag: "v0.12.4", API: s.URL},
			asset:   "lnav-0.12.4-linux-musl-x86_64.zip",
			wantErr: "lnav-0.12.4-linux-musl-x86_64.zip has no checksum: its release has no checksums file and predates GitHub's asset digests (June 2025); pin a newer version",
		},
		{
			name:    "no such file",
			digest:  protoc,
			asset:   "protoc-36.2-linux-riscv64.zip",
			wantErr: "release v36.2 of protocolbuffers/protobuf has no file protoc-36.2-linux-riscv64.zip",
		},
		{
			name:    "no such release",
			digest:  GitHubDigest{Repo: "tstack/lnav", Tag: "v9.9.9", API: s.URL},
			asset:   "lnav-9.9.9-linux-musl-x86_64.zip",
			wantErr: "tstack/lnav has no release v9.9.9 on GitHub",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.digest.SHA256(t.Context(), c, Asset{Name: tt.asset})
			check(t, got, err, "", tt.wantErr)
		})
	}
	_, err = GitHubDigest{Repo: "tstack/lnav", Tag: "v0.12.4", API: s.URL}.SHA256(t.Context(), c, Asset{Name: "lnav-0.12.4-linux-musl-x86_64.zip"})
	if _, ok := errors.AsType[*NoChecksumError](err); !ok {
		t.Errorf("SHA256() of a file without a digest: error = %v, want a *NoChecksumError", err)
	}
}

func TestDigestsNewerFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digests.json")
	if err := os.WriteFile(path, []byte(`{"format_version": 2, "digests": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Digests{Path: path}
	if err := d.Load(); err == nil || !strings.Contains(err.Error(), "newer version of maisys") {
		t.Errorf("Load() error = %v, want one asking to update maisys", err)
	}
	c := &Client{Digests: d}
	_, err := GitHubDigest{Repo: "a/b", Tag: "v1"}.SHA256(t.Context(), c, Asset{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "newer version of maisys") {
		t.Errorf("SHA256() error = %v, want one asking to update maisys", err)
	}
}
