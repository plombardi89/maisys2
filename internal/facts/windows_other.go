//go:build !windows

package facts

import (
	"context"
	"errors"

	"github.com/plombardi89/maisys2/internal/system"
)

// DetectWindows detects the details of the Windows host maisys is running
// on. Elsewhere, there are none.
func DetectWindows(context.Context, system.Runner) (*Windows, error) {
	return nil, errors.New("only a Windows host has Windows details to detect")
}
