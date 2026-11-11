package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Format is how a download is packed.
type Format int

// The formats downloads come in.
const (
	// Binary is a download that's the program itself.
	Binary Format = iota
	// TarGz is a gzipped tar archive.
	TarGz
	// Zip is a zip archive.
	Zip
)

// String names the format.
func (f Format) String() string {
	switch f {
	case Binary:
		return "binary"
	case TarGz:
		return "tar.gz"
	case Zip:
		return "zip"
	default:
		return fmt.Sprintf("Format(%d)", int(f))
	}
}

// Archive says how to unpack a download.
type Archive struct {
	// Format is how the download is packed.
	Format Format
	// Strip is how many leading folders to drop from the paths in the
	// archive, as tar's --strip-components does: 1 for an archive that
	// holds everything in a top-level folder such as "lnav-0.14.1/".
	// Entries with no more than that in their path are skipped.
	Strip int
	// Binary is the file name a Binary download is given.
	Binary string
}

// Unpack unpacks the download at src into the folder dir, which must not
// exist: into a temporary folder next to dir first, which is then renamed
// to dir, so dir never holds part of an archive. An entry whose path, or
// link, would leave dir is an error, even through other links. Files the
// archive marks executable get mode 0755, other files 0644 and folders
// 0755 (less the umask).
func (a Archive) Unpack(src, dir string) (err error) {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	x := &extractor{root: root, strip: a.Strip}
	switch a.Format {
	case Binary:
		err = x.binary(src, a.Binary)
	case TarGz:
		err = x.tarGz(src)
	case Zip:
		err = x.zip(src)
	default:
		err = fmt.Errorf("can't unpack a download in format %s", a.Format)
	}
	if err != nil {
		return fmt.Errorf("unpacking %s: %w", filepath.Base(src), err)
	}
	if err := x.checkLinks(); err != nil {
		return fmt.Errorf("unpacking %s: %w", filepath.Base(src), err)
	}
	if err := root.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// extractor writes an archive's entries inside root, which keeps them from
// leaving it, even through symlinks.
type extractor struct {
	root  *os.Root
	strip int
}

// binary copies the program at src to name.
func (x *extractor) binary(src, name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%q isn't a valid file name for a program", name)
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return x.write(name, f, 0o755)
}

func (x *extractor) tarGz(src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := x.path(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = x.root.MkdirAll(rel, 0o755)
		case tar.TypeReg:
			err = x.write(rel, tr, hdr.FileInfo().Mode())
		case tar.TypeSymlink:
			err = x.symlink(rel, hdr.Linkname)
		case tar.TypeLink:
			err = x.hardlink(rel, hdr.Linkname)
		default:
			// Devices, FIFOs and pax headers have no place in a tool.
		}
		if err != nil {
			return fmt.Errorf("%s: %w", hdr.Name, err)
		}
	}
}

func (x *extractor) zip(src string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if err := x.zipEntry(f); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

func (x *extractor) zipEntry(f *zip.File) error {
	rel, err := x.path(f.Name)
	if err != nil || rel == "" {
		return err
	}
	mode := f.Mode()
	switch {
	case mode.IsDir():
		return x.root.MkdirAll(rel, 0o755)
	case mode&fs.ModeSymlink != 0:
		target, err := readZipEntry(f, 4096)
		if err != nil {
			return err
		}
		return x.symlink(rel, string(target))
	case mode.IsRegular():
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		return x.write(rel, rc, mode)
	default:
		return nil
	}
}

func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// path returns where an entry named name goes, relative to the root and
// with Strip folders dropped, or "" if it's skipped.
func (x *extractor) path(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("%s has an absolute path", name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s is outside the archive's folder", name)
	}
	if clean == "." {
		return "", nil
	}
	parts := strings.Split(clean, "/")
	if len(parts) <= x.strip {
		return "", nil
	}
	return filepath.FromSlash(path.Join(parts[x.strip:]...)), nil
}

// write writes a file at rel, executable if mode says so.
func (x *extractor) write(rel string, r io.Reader, mode fs.FileMode) (err error) {
	if err := x.parent(rel); err != nil {
		return err
	}
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	f, err := x.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(f, r)
	return err
}

// symlink makes a symlink at rel pointing to target, which must be a
// relative path that stays inside the root.
func (x *extractor) symlink(rel, target string) error {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
		return fmt.Errorf("links to %q, outside the archive's folder", target)
	}
	resolved := path.Clean(path.Join(path.Dir(filepath.ToSlash(rel)), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("links to %q, outside the archive's folder", target)
	}
	if err := x.parent(rel); err != nil {
		return err
	}
	return x.root.Symlink(target, rel)
}

// checkLinks fails if a symlink in the root leads outside it. One can
// through others, such as b → a/.. with a → ., though each link's own
// target stays inside. Links to nothing inside the root are fine.
func (x *extractor) checkLinks() error {
	return fs.WalkDir(x.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		rel := filepath.FromSlash(name)
		if _, err := x.root.Stat(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			target, _ := x.root.Readlink(rel)
			return fmt.Errorf("%s: links to %q, which leaves the archive's folder or can't be followed: %w", name, target, err)
		}
		return nil
	})
}

// hardlink makes a hard link at rel to the entry named name, which the
// archive has already unpacked.
func (x *extractor) hardlink(rel, name string) error {
	old, err := x.path(name)
	if err != nil {
		return err
	}
	if old == "" {
		return fmt.Errorf("links to %s, which isn't unpacked", name)
	}
	if err := x.parent(rel); err != nil {
		return err
	}
	return x.root.Link(old, rel)
}

// parent creates the folder rel goes in.
func (x *extractor) parent(rel string) error {
	if dir := filepath.Dir(rel); dir != "." {
		return x.root.MkdirAll(dir, 0o755)
	}
	return nil
}

// RemoveAll deletes path and everything in it, as os.RemoveAll does, first
// making folders writable if that's what stops it, as an archive's
// read-only folders would.
func RemoveAll(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(path)
}
