// Package fsutil holds file helpers that packages which don't depend on
// each other share, such as the state file's, the local settings' and the
// caches'. It imports nothing else from maisys, so any package can use it.
package fsutil

// Rename renames oldpath to newpath, replacing newpath if it exists, as
// os.Rename does. On Windows, replacing a file fails while another process
// has it open without allowing it to be deleted, as maisys status reading
// the state file or an antivirus scanning a file that was just written may
// have: there, Rename tries again a few times, for up to about a second
// and a half, before it gives up. Elsewhere it's os.Rename.
func Rename(oldpath, newpath string) error {
	return rename(oldpath, newpath)
}
