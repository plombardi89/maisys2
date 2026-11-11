package download

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// entry is an entry of a test archive.
type entry struct {
	name string
	body string
	mode fs.FileMode // fs.ModeDir for a folder, fs.ModeSymlink for a symlink to body
	link string      // for a hard link, the entry it links to
}

func tarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm())}
		switch {
		case e.link != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeLink, e.link
		case e.mode&fs.ModeDir != 0:
			hdr.Typeflag = tar.TypeDir
		case e.mode&fs.ModeSymlink != 0:
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.body
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipFile(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		hdr.SetMode(e.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if e.mode&fs.ModeDir == 0 {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeTemp writes data to a temporary file and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tree returns the files in dir: each file's relative path with its
// contents, each folder's with a trailing slash, and each symlink's with
// "-> target".
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var list []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			list = append(list, rel+" -> "+filepath.ToSlash(target))
		case d.IsDir():
			list = append(list, rel+"/")
		default:
			data, _ := os.ReadFile(path)
			list = append(list, rel+": "+string(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestUnpack(t *testing.T) {
	lnav := []entry{
		{name: "lnav-0.14.1/", mode: fs.ModeDir | 0o755},
		{name: "lnav-0.14.1/lnav.1", body: "manual", mode: 0o644},
		{name: "lnav-0.14.1/lnav", body: "program", mode: 0o755},
	}
	tests := []struct {
		name    string
		file    string
		data    func(t *testing.T) []byte
		archive Archive
		want    []string
	}{
		{
			name:    "binary",
			file:    "yq_linux_amd64",
			data:    func(*testing.T) []byte { return []byte("program") },
			archive: Archive{Format: Binary, Binary: "yq"},
			want:    []string{"yq: program"},
		},
		{
			name: "tar.gz",
			file: "go1.27.3.linux-amd64.tar.gz",
			data: func(t *testing.T) []byte {
				t.Helper()
				return tarGz(t, []entry{
					{name: "go/", mode: fs.ModeDir | 0o755},
					{name: "go/bin/go", body: "go", mode: 0o755},
					{name: "go/bin/gofmt", body: "gofmt", mode: 0o755},
				})
			},
			archive: Archive{Format: TarGz},
			want:    []string{"go/", "go/bin/", "go/bin/go: go", "go/bin/gofmt: gofmt"},
		},
		{
			name: "tar.gz with a top-level folder stripped",
			file: "golangci-lint-2.15.0-linux-amd64.tar.gz",
			data: func(t *testing.T) []byte {
				t.Helper()
				return tarGz(t, []entry{
					{name: "golangci-lint-2.15.0-linux-amd64/LICENSE", body: "license", mode: 0o644},
					{name: "golangci-lint-2.15.0-linux-amd64/golangci-lint", body: "program", mode: 0o755},
					{name: "pax_global_header", body: "ignored", mode: 0o644},
				})
			},
			archive: Archive{Format: TarGz, Strip: 1},
			want:    []string{"LICENSE: license", "golangci-lint: program"},
		},
		{
			name: "tar.gz with a top-level folder entry stripped",
			file: "uv-x86_64-unknown-linux-gnu.tar.gz",
			data: func(t *testing.T) []byte {
				t.Helper()
				return tarGz(t, []entry{
					{name: "uv-x86_64-unknown-linux-gnu/", mode: fs.ModeDir | 0o755},
					{name: "uv-x86_64-unknown-linux-gnu/uv", body: "uv", mode: 0o755},
					{name: "uv-x86_64-unknown-linux-gnu/uvx", body: "uvx", mode: 0o755},
				})
			},
			archive: Archive{Format: TarGz, Strip: 1},
			want:    []string{"uv: uv", "uvx: uvx"},
		},
		{
			name: "tar.gz with ./",
			file: "yq_linux_amd64.tar.gz",
			data: func(t *testing.T) []byte {
				t.Helper()
				return tarGz(t, []entry{{name: "./yq_linux_amd64", body: "program", mode: 0o755}})
			},
			archive: Archive{Format: TarGz},
			want:    []string{"yq_linux_amd64: program"},
		},
		{
			name: "zip with a top-level folder stripped",
			file: "lnav-0.14.1-linux-musl-x86_64.zip",
			data: func(t *testing.T) []byte {
				t.Helper()
				return zipFile(t, lnav)
			},
			archive: Archive{Format: Zip, Strip: 1},
			want:    []string{"lnav: program", "lnav.1: manual"},
		},
		{
			name: "zip with read-only files",
			file: "protoc-36.2-linux-x86_64.zip",
			data: func(t *testing.T) []byte {
				t.Helper()
				return zipFile(t, []entry{
					{name: "bin/", mode: fs.ModeDir | 0o555},
					{name: "bin/protoc", body: "program", mode: 0o555},
					{name: "include/google/protobuf/any.proto", body: "proto", mode: 0o444},
				})
			},
			archive: Archive{Format: Zip},
			want:    []string{"bin/", "bin/protoc: program", "include/", "include/google/", "include/google/protobuf/", "include/google/protobuf/any.proto: proto"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := writeTemp(t, tt.file, tt.data(t))
			parent := t.TempDir()
			dir := filepath.Join(parent, "tools", "1.0.0")
			if err := tt.archive.Unpack(src, dir); err != nil {
				t.Fatal(err)
			}
			if got := tree(t, dir); !slices.Equal(got, tt.want) {
				t.Errorf("unpacked\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
			if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) != 1 {
				t.Errorf("Unpack() left %d entries next to the folder, want only the folder", len(entries))
			}
		})
	}
}

func TestUnpackModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit")
	}
	src := writeTemp(t, "protoc.zip", zipFile(t, []entry{
		{name: "bin/", mode: fs.ModeDir | 0o555},
		{name: "bin/protoc", body: "program", mode: 0o555},
		{name: "readme.txt", body: "text", mode: 0o600},
	}))
	dir := filepath.Join(t.TempDir(), "protoc")
	if err := (Archive{Format: Zip}).Unpack(src, dir); err != nil {
		t.Fatal(err)
	}
	umask := umaskOf(t)
	for name, want := range map[string]fs.FileMode{".": 0o755, "bin": 0o755, "bin/protoc": 0o755, "readme.txt": 0o644} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want&^umask {
			t.Errorf("%s has mode %v, want %v", name, got, want&^umask)
		}
	}
	if err := RemoveAll(dir); err != nil {
		t.Errorf("RemoveAll() = %v", err)
	}
}

// umaskOf returns the process's umask, as it applies to new files.
func umaskOf(t *testing.T) fs.FileMode {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, nil, 0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return 0o777 &^ info.Mode().Perm()
}

func TestUnpackLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tools with symlinks are only unpacked on Linux")
	}
	src := writeTemp(t, "idea.tar.gz", tarGz(t, []entry{
		{name: "idea-IU-263.7307.200/bin/idea", body: "launcher", mode: 0o755},
		{name: "idea-IU-263.7307.200/jbr/lib/libjvm.so", body: "jvm", mode: 0o644},
		{name: "idea-IU-263.7307.200/jbr/lib/server/libjvm.so", mode: fs.ModeSymlink, body: "../libjvm.so"},
		{name: "idea-IU-263.7307.200/bin/idea2", link: "idea-IU-263.7307.200/bin/idea"},
	}))
	dir := filepath.Join(t.TempDir(), "intellij")
	if err := (Archive{Format: TarGz, Strip: 1}).Unpack(src, dir); err != nil {
		t.Fatal(err)
	}
	want := []string{"bin/", "bin/idea: launcher", "bin/idea2: launcher", "jbr/", "jbr/lib/", "jbr/lib/libjvm.so: jvm", "jbr/lib/server/", "jbr/lib/server/libjvm.so -> ../libjvm.so"}
	if got := tree(t, dir); !slices.Equal(got, want) {
		t.Errorf("unpacked\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	zipSrc := writeTemp(t, "links.zip", zipFile(t, []entry{
		{name: "tool/bin/real", body: "program", mode: 0o755},
		{name: "tool/run", body: "bin/real", mode: fs.ModeSymlink | 0o777},
	}))
	zipDir := filepath.Join(t.TempDir(), "links")
	if err := (Archive{Format: Zip, Strip: 1}).Unpack(zipSrc, zipDir); err != nil {
		t.Fatal(err)
	}
	if got, want := tree(t, zipDir), []string{"bin/", "bin/real: program", "run -> bin/real"}; !slices.Equal(got, want) {
		t.Errorf("unpacked %q, want %q", got, want)
	}
}

func TestUnpackLinkChains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tools with symlinks are only unpacked on Linux")
	}
	tests := []struct {
		name    string
		entries []entry
		wantErr string // "" means it unpacks
	}{
		{
			name: "out of the folder through another link",
			entries: []entry{
				{name: "tool/a", body: ".", mode: fs.ModeSymlink},
				{name: "tool/b", body: "a/..", mode: fs.ModeSymlink},
			},
			wantErr: `b: links to "a/..", which leaves the archive's folder`,
		},
		{
			name: "out of the folder through a chain",
			entries: []entry{
				{name: "tool/sub/", mode: fs.ModeDir | 0o755},
				{name: "tool/sub/up", body: "..", mode: fs.ModeSymlink},
				{name: "tool/top", body: "sub/up", mode: fs.ModeSymlink},
				{name: "tool/escape", body: "top/sub/up/..", mode: fs.ModeSymlink},
			},
			wantErr: `escape: links to "top/sub/up/..", which leaves the archive's folder`,
		},
		{
			name: "inside through other links",
			entries: []entry{
				{name: "tool/lib/libjvm.so", body: "jvm", mode: 0o644},
				{name: "tool/current", body: "lib", mode: fs.ModeSymlink},
				{name: "tool/jvm", body: "current/../lib/libjvm.so", mode: fs.ModeSymlink},
			},
		},
		{
			name:    "to nothing",
			entries: []entry{{name: "tool/dangling", body: "missing/file", mode: fs.ModeSymlink}},
		},
	}
	for _, tt := range tests {
		for _, format := range []Format{TarGz, Zip} {
			t.Run(tt.name+" "+format.String(), func(t *testing.T) {
				data := tarGz(t, tt.entries)
				if format == Zip {
					data = zipFile(t, tt.entries)
				}
				src := writeTemp(t, "tool."+format.String(), data)
				parent := t.TempDir()
				err := Archive{Format: format, Strip: 1}.Unpack(src, filepath.Join(parent, "1.0.0"))
				if tt.wantErr == "" {
					if err != nil {
						t.Fatalf("Unpack() error = %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Unpack() error = %v, want one containing %q", err, tt.wantErr)
				}
				if entries, _ := os.ReadDir(parent); len(entries) != 0 {
					t.Errorf("Unpack() left %v behind", entries)
				}
			})
		}
	}
}

func TestUnpackRejects(t *testing.T) {
	tests := []struct {
		name    string
		format  Format
		entries []entry
		strip   int
		wantErr string
	}{
		{
			name:    "a path out of the folder",
			format:  TarGz,
			entries: []entry{{name: "../../evil", body: "x", mode: 0o644}},
			wantErr: "../../evil is outside the archive's folder",
		},
		{
			name:    "a zip path out of the folder",
			format:  Zip,
			entries: []entry{{name: "tool/../../evil", body: "x", mode: 0o644}},
			wantErr: "is outside the archive's folder",
		},
		{
			name:    "an absolute path",
			format:  TarGz,
			entries: []entry{{name: "/etc/evil", body: "x", mode: 0o644}},
			wantErr: "/etc/evil has an absolute path",
		},
		{
			name:    "a link to an absolute path",
			format:  TarGz,
			entries: []entry{{name: "tool/evil", body: "/etc/passwd", mode: fs.ModeSymlink}},
			wantErr: `links to "/etc/passwd", outside the archive's folder`,
		},
		{
			name:    "a link out of the folder",
			format:  TarGz,
			entries: []entry{{name: "tool/bin/evil", body: "../../../etc", mode: fs.ModeSymlink}},
			strip:   1,
			wantErr: `links to "../../../etc", outside the archive's folder`,
		},
		{
			name:    "a link into a stripped folder",
			format:  TarGz,
			entries: []entry{{name: "tool/evil", body: "../other", mode: fs.ModeSymlink}},
			strip:   1,
			wantErr: `links to "../other", outside the archive's folder`,
		},
		{
			name:    "a hard link out of the folder",
			format:  TarGz,
			entries: []entry{{name: "tool/evil", link: "../outside"}},
			wantErr: "../outside is outside the archive's folder",
		},
		{
			name:    "a file twice",
			format:  TarGz,
			entries: []entry{{name: "tool", body: "x", mode: 0o644}, {name: "tool", body: "y", mode: 0o644}},
			wantErr: "tool: ",
		},
		{
			name:    "not an archive",
			format:  Zip,
			wantErr: "unpacking tool.zip: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var data []byte
			switch {
			case tt.entries == nil:
				data = []byte("not an archive")
			case tt.format == Zip:
				data = zipFile(t, tt.entries)
			default:
				data = tarGz(t, tt.entries)
			}
			src := writeTemp(t, "tool."+map[Format]string{Zip: "zip", TarGz: "tar.gz"}[tt.format], data)
			parent := t.TempDir()
			dir := filepath.Join(parent, "1.0.0")
			err := Archive{Format: tt.format, Strip: tt.strip}.Unpack(src, dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Unpack() error = %v, want one containing %q", err, tt.wantErr)
			}
			if entries, _ := os.ReadDir(parent); len(entries) != 0 {
				t.Errorf("Unpack() left %v behind", entries)
			}
		})
	}

	if err := (Archive{Format: Binary, Binary: "../x"}).Unpack(writeTemp(t, "x", nil), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("Unpack() of a binary with an invalid name worked")
	}
}
