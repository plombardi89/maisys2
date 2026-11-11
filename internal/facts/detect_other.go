//go:build !linux && !windows

package facts

import "runtime"

// detectOS reports an unsupported OS: maisys only runs on Linux and Windows.
func detectOS() (*System, error) {
	return &System{OSID: runtime.GOOS, PrettyName: runtime.GOOS}, nil
}
