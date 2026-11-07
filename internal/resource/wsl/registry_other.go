//go:build !windows

package wsl

import "errors"

// errNotWindows is what the live Registry returns outside Windows.
var errNotWindows = errors.New("WSL hosts run Windows")

// Live returns a Registry that fails: only Windows has one.
func Live() Registry { return otherRegistry{} }

type otherRegistry struct{}

func (otherRegistry) Distros() ([]Registered, error) { return nil, errNotWindows }
func (otherRegistry) DefaultDistro() (string, error) { return "", errNotWindows }
func (otherRegistry) Version() (string, error)       { return "", errNotWindows }
func (otherRegistry) RebootPending() (bool, error)   { return false, errNotWindows }
func (otherRegistry) GitDir() (string, error)        { return "", errNotWindows }
