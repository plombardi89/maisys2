// Package cachefile reads and writes maisys's JSON cache files, such as
// latest.json and digests.json. Each records a format_version, and is
// replaced in one step when it's written, so it's never left half written.
package cachefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/plombardi89/maisys2/internal/fsutil"
)

// Load reads the cache file at path into v, which must have a
// format_version field. It returns false if there's no file, or if the file
// can't be read as a cache, which is then treated as empty and replaced the
// next time it's saved. A file in a newer format than current is an error:
// it was written by a newer maisys.
func Load(path string, current int, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	var head struct {
		FormatVersion int `json:"format_version"`
	}
	if !decodes(data, &head) || head.FormatVersion < 1 {
		return false, nil
	}
	if head.FormatVersion > current {
		return false, fmt.Errorf("%s was written by a newer version of maisys (format_version %d; this version reads up to %d): update maisys and try again",
			path, head.FormatVersion, current)
	}
	// No older formats exist yet: an older file is just an empty cache.
	return head.FormatVersion == current && decodes(data, v), nil
}

// decodes reports whether data decodes into v.
func decodes(data []byte, v any) bool {
	return json.Unmarshal(data, v) == nil
}

// Save writes v to path as indented JSON, creating its folder if needed: to
// a temporary file first, which is then renamed over path.
func Save(path string, v any) (err error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			err = fmt.Errorf("saving %s: %w", path, err)
		}
	}()
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fsutil.Rename(f.Name(), path)
}
