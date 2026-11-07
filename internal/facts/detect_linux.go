//go:build linux

package facts

import "os"

func detectOS() (*System, error) {
	return DetectLinux(os.ReadFile, os.Getenv)
}
