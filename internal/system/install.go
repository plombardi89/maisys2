package system

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InstallFile writes data to dst as a file owned by root, with mode's
// permission bits, the way maisys writes root-owned files: to a temporary
// file first, which "sudo install -m MODE -o root -g root" then puts in
// place. dst must be an absolute path in a folder that exists. Like any
// command run as root, it fails with ErrNeedsPassword if no sudo session
// is started and sudo needs a password.
func InstallFile(ctx context.Context, r Runner, data []byte, dst string, mode fs.FileMode) error {
	return InstallFileChecked(ctx, r, data, dst, mode, nil)
}

// InstallFileChecked is InstallFile, but first runs check, if it isn't
// nil, on the temporary file that's put in place, such as visudo -cf for a
// sudoers rule. If check fails, dst is left alone.
func InstallFileChecked(ctx context.Context, r Runner, data []byte, dst string, mode fs.FileMode, check func(ctx context.Context, tmp string) error) error {
	if !sudoSupported {
		return errNoSudo
	}
	if !filepath.IsAbs(dst) {
		return fmt.Errorf("can't write %s as root: it isn't an absolute path", dst)
	}
	tmp, err := os.CreateTemp("", "maisys-*")
	if err != nil {
		return fmt.Errorf("couldn't write %s: %w", dst, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("couldn't write %s: %w", dst, err)
	}
	if check != nil {
		if err := check(ctx, tmp.Name()); err != nil {
			return fmt.Errorf("didn't write %s: %w", dst, err)
		}
	}

	_, err = r.Run(ctx, Cmd{
		Name:  "install",
		Args:  []string{"-m", fmt.Sprintf("%04o", uint32(mode.Perm())), "-o", "root", "-g", "root", tmp.Name(), dst},
		Root:  true,
		Label: "writing " + dst,
	})
	return err
}
