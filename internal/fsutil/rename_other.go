//go:build !windows

package fsutil

import "os"

func rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
