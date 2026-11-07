package file

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"text/template"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
)

// Content returns what a file holds. It's called when the file is checked,
// and again when it's applied.
type Content func(rt *resource.Runtime) ([]byte, error)

// Bytes returns Content that's always data, for files a module generates.
func Bytes(data []byte) Content {
	return func(*resource.Runtime) ([]byte, error) { return data, nil }
}

// TemplateData is what a template can use.
type TemplateData struct {
	// Vars are the values from [files.vars].
	Vars map[string]string
	// Facts is the live system.
	Facts *facts.System
	// Machine is the machine's merged config.
	Machine *config.Machine
}

// Template returns Content rendered from text, a Go text/template, with
// vars as .Vars and the runtime's facts and machine as .Facts and
// .Machine. Errors name the template name, and a key missing from .Vars is
// an error. Templates have only text/template's built-in functions, so
// they can't read environment variables or files: that keeps secrets out
// of the plan and the state file.
func Template(name, text string, vars map[string]string) Content {
	return func(rt *resource.Runtime) ([]byte, error) {
		return render(name, text, vars, rt)
	}
}

func render(name, text string, vars map[string]string, rt *resource.Runtime) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, err
	}
	if vars == nil {
		vars = map[string]string{}
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, TemplateData{Vars: vars, Facts: rt.Facts, Machine: rt.Machine}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Source is a file or a directory in the config repo to copy into the home
// folder, as a files item describes it.
type Source struct {
	// FS is the config repo.
	FS fs.FS
	// Path is the file or directory in FS. A directory's path ends in /.
	Path string
	// Target is where the file goes, such as "~/.gitconfig", or for a
	// directory the folder its files go in, such as "~/.config/nvim/".
	Target string
	// Template renders each file as a Go text/template (see Template).
	Template bool
	// Vars are the templates' .Vars.
	Vars map[string]string
	// Mode, if it isn't zero, sets each file's permissions. Otherwise each
	// file keeps the executable bit it has in the config repo.
	Mode fs.FileMode
	// CRLF converts line endings to CRLF.
	CRLF bool
}

// Files returns a File for each file s copies: the source file, or every
// file in the source directory and the folders in it, in order. Each is
// checked and recorded on its own, and files in the target folder that
// don't come from the source are left alone.
func (s Source) Files() ([]*File, error) {
	dir, isDir := strings.CutSuffix(s.Path, "/")
	if !isDir {
		info, err := fs.Stat(s.FS, s.Path)
		if err != nil {
			return nil, fmt.Errorf("reading %s in the config repo: %w", s.Path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s in the config repo isn't a file", s.Path)
		}
		return []*File{s.file(s.Path, s.Target, info)}, nil
	}

	target := strings.TrimSuffix(s.Target, "/")
	var files []*File
	err := fs.WalkDir(s.FS, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// A link is copied as the file it points to; links to folders are
		// left out.
		info, err := fs.Stat(s.FS, name)
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		rel := strings.TrimPrefix(name, dir+"/")
		files = append(files, s.file(name, path.Join(target, rel), info))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s in the config repo: %w", s.Path, err)
	}
	return files, nil
}

// file returns the File for name, a file in the config repo described by
// info, copied to target.
func (s Source) file(name, target string, info fs.FileInfo) *File {
	fsys, tmpl, vars := s.FS, s.Template, s.Vars
	return &File{
		Path: target,
		Content: func(rt *resource.Runtime) ([]byte, error) {
			data, err := fs.ReadFile(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("reading %s in the config repo: %w", name, err)
			}
			if !tmpl {
				return data, nil
			}
			return render(name, string(data), vars, rt)
		},
		Mode:       s.Mode,
		Executable: info.Mode()&0o111 != 0,
		CRLF:       s.CRLF,
	}
}

// ParseMode reads a file mode written as octal digits, such as "0600". An
// empty string is zero: no mode. Only permission bits are allowed.
func ParseMode(s string) (fs.FileMode, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("the mode must be octal permission bits such as \"0600\", not %q", s)
	}
	return fs.FileMode(n), nil
}

// toCRLF converts data's line endings to CRLF, leaving those that are
// already CRLF alone.
func toCRLF(data []byte) []byte {
	lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
}
