//go:build !windows

package system

import (
	"context"
	"errors"
)

// newAdminSession returns nil: only Windows runs commands as admin, rather
// than as root with sudo.
func newAdminSession(*ExecRunner) *adminSession {
	return nil
}

// ServeElevated runs the elevated helper, which only Windows has.
func ServeElevated(context.Context, string, string) error {
	return errors.New("the elevated helper only runs on Windows")
}
