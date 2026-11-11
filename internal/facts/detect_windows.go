//go:build windows

package facts

import (
	"strconv"

	"golang.org/x/sys/windows"

	"github.com/plombardi89/maisys2/internal/config"
)

func detectOS() (*System, error) {
	build := windows.RtlGetVersion().BuildNumber
	return &System{
		OS:         config.Windows,
		OSID:       string(config.Windows),
		OSVersion:  strconv.FormatUint(uint64(build), 10),
		PrettyName: windowsName(build),
	}, nil
}
