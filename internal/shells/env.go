// Package shells holds what modules add to the shell environment, and the
// adapters that write it out for each shell (bash, zsh and fish).
//
// Modules describe their environment as data (see Contributor): folders to
// add to PATH, environment variables, and init steps with a version for
// each shell. The shell module collects them from every enabled module, and
// each enabled shell's adapter writes them in its own syntax.
package shells

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
)

// Env is what one module adds to the environment of every enabled shell.
type Env struct {
	// Path lists folders to add to the front of PATH, in order. They may
	// start with "~/" for the home folder, or refer to variables set
	// earlier as $NAME, such as "$GOPATH/bin".
	Path []string
	// Variables are environment variables to export, in order. Values
	// may start with "~/" and refer to $HOME and variables set earlier as
	// $NAME; everything else is taken literally.
	Variables []Variable
	// Init are steps to run when a shell starts, such as SDKMAN's init
	// script.
	Init []InitStep
}

// Variable is an environment variable to export.
type Variable struct {
	Name, Value string
}

// InitStep is something to run when a shell starts, with a version for
// each shell. A shell without a version skips the step, and the plan warns
// about it (for example "sdkman: no fish integration").
type InitStep struct {
	// Name names the step in warnings, such as "sdkman".
	Name string
	// Bash, Zsh and Fish are the step in each shell's syntax. Empty means
	// that shell has no version of it.
	Bash, Zsh, Fish string
	// Last marks steps that must run after every other step, such as
	// SDKMAN's, which has to come last.
	Last bool
}

// Contributor is implemented by modules that add to the shell environment.
// The shell module asks every enabled module that implements it.
type Contributor interface {
	// ShellEnv returns what the module adds for cfg, its merged config, on
	// the sync's live system.
	ShellEnv(cfg config.ModuleConfig, rt *resource.Runtime) (Env, error)
}

// Merge returns the environments of envs together, in order: their PATH
// folders, each once, then their variables and their init steps.
func Merge(envs ...Env) Env {
	var merged Env
	for _, env := range envs {
		for _, dir := range env.Path {
			if !slices.Contains(merged.Path, dir) {
				merged.Path = append(merged.Path, dir)
			}
		}
		merged.Variables = append(merged.Variables, env.Variables...)
		merged.Init = append(merged.Init, env.Init...)
	}
	return merged
}

// Check checks that the shells can write env: that its folders and its
// variables' names are valid, and that every init step has a name.
func (e Env) Check() error {
	var errs []error
	for _, dir := range e.Path {
		if dir == "" || strings.ContainsAny(dir, ":\n") {
			errs = append(errs, fmt.Errorf("%q can't be added to PATH", dir))
		}
	}
	for _, v := range e.Variables {
		if !ValidName(v.Name) {
			errs = append(errs, fmt.Errorf("%q isn't a valid variable name", v.Name))
		}
	}
	for _, step := range e.Init {
		if strings.TrimSpace(step.Name) == "" {
			errs = append(errs, errors.New("an init step has no name"))
		}
	}
	return errors.Join(errs...)
}
