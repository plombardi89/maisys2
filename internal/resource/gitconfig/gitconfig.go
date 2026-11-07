// Package gitconfig manages single git settings in ~/.config/git/config,
// which git reads along with ~/.gitconfig. maisys never touches
// ~/.gitconfig: that's left to you (or to the files module).
package gitconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Setting is.
const Kind = "git-setting"

// File is where git settings go.
const File = "~/.config/git/config"

// The keys of a git setting record's Details.
const (
	// DetailKey is the setting's key, such as "credential.helper".
	DetailKey = "key"
	// DetailFile is the file the setting is in.
	DetailFile = "file"
)

// Setting is one git setting in a git config file, ~/.config/git/config
// unless File says otherwise. A key with several values, such as
// credential.helper, has all its values replaced with Values.
type Setting struct {
	// Key is the setting, such as "credential.helper".
	Key string
	// Values are the setting's values, in order. Most settings have one.
	Values []string
	// File is the git config file, "~/..." in your home folder or an
	// absolute path. Empty means ~/.config/git/config.
	File string
}

var _ resource.Resource = (*Setting)(nil)

// ID returns the setting's ID, such as "git-setting:credential.helper".
func (s *Setting) ID() string { return Kind + ":" + s.Key }

// Kind returns "git-setting".
func (s *Setting) Kind() string { return Kind }

// Targets returns nothing: the config file holds other settings too.
func (s *Setting) Targets() []string { return nil }

// Privileged returns false: the setting is in your own config file.
func (s *Setting) Privileged() bool { return false }

// file returns the setting's file, as the plan shows it.
func (s *Setting) file() string {
	if s.File == "" {
		return File
	}
	return file.Display(s.File)
}

// Check compares the setting's values with the ones it should have.
func (s *Setting) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if s.Key == "" || len(s.Values) == 0 {
		return resource.Change{}, fmt.Errorf("git setting %q needs a key and at least one value", s.Key)
	}
	path, err := file.Expand(rt.Paths.Home, s.file())
	if err != nil {
		return resource.Change{}, err
	}
	current, err := get(ctx, rt.Runner, path, s.Key)
	switch {
	case err != nil:
		return resource.Change{}, err
	case slices.Equal(current, s.Values):
		return resource.Change{}, nil
	case len(current) == 0:
		return resource.Change{Action: resource.Create, Summary: fmt.Sprintf("git %s = %s", s.Key, show(s.Values))}, nil
	default:
		return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("git %s %s → %s", s.Key, show(current), show(s.Values))}, nil
	}
}

// Apply replaces the setting's values with Values, creating the config
// file's folder if it's missing.
func (s *Setting) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	rec := state.Record{Details: map[string]string{DetailKey: s.Key, DetailFile: s.file()}}
	c, err := s.Check(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}
	if c.Action == resource.NoChange {
		return rec, nil
	}
	path, err := file.Expand(rt.Paths.Home, s.file())
	if err != nil {
		return state.Record{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return state.Record{}, fmt.Errorf("making the folder for %s: %w", s.file(), err)
	}
	if _, err := rt.Runner.Run(ctx, gitConfig(path, "--replace-all", s.Key, s.Values[0])); err != nil {
		return state.Record{}, err
	}
	for _, v := range s.Values[1:] {
		if _, err := rt.Runner.Run(ctx, gitConfig(path, "--add", s.Key, v)); err != nil {
			return state.Record{}, err
		}
	}
	return rec, nil
}

// get returns the values of key in the git config file at path.
func get(ctx context.Context, r system.Runner, path, key string) ([]string, error) {
	c := gitConfig(path, "--null", "--get-all", key)
	// git exits with 1 if the key isn't set, or the file isn't there.
	c.AllowExit = []int{1}
	res, err := r.Run(ctx, c)
	if err != nil || res.ExitCode != 0 {
		return nil, err
	}
	values := strings.Split(string(res.Stdout), "\x00")
	return values[:len(values)-1], nil
}

// gitConfig returns the git config command for the file at path with args.
func gitConfig(path string, args ...string) system.Cmd {
	return system.Cmd{
		Name:  "git",
		Args:  append([]string{"config", "--file", path}, args...),
		Label: "git config " + args[0],
	}
}

// show returns values as the plan shows them: a single value as it is,
// and several quoted.
func show(values []string) string {
	if len(values) == 1 && values[0] != "" && !strings.ContainsAny(values[0], "\"\n") {
		return values[0]
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, ", ")
}

// Remover unsets git settings once they're taken out of the config.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "git-setting".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the setting rec records is still set.
func (Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	path, key, err := recorded(rt, rec)
	if err != nil {
		return resource.Change{}, err
	}
	values, err := get(ctx, rt.Runner, path, key)
	if err != nil || len(values) == 0 {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Remove, Summary: "git " + key}, nil
}

// Remove unsets the setting rec records.
func (Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	path, key, err := recorded(rt, rec)
	if err != nil {
		return err
	}
	c := gitConfig(path, "--unset-all", key)
	// git exits with 5 if the key isn't set.
	c.AllowExit = []int{5}
	_, err = rt.Runner.Run(ctx, c)
	return err
}

// recorded returns the file and key of the setting rec records.
func recorded(rt *resource.Runtime, rec state.Record) (path, key string, err error) {
	key = rec.Details[DetailKey]
	if key == "" {
		return "", "", errors.New("the state file's record has no key")
	}
	name := rec.Details[DetailFile]
	if name == "" {
		name = File
	}
	path, err = file.Expand(rt.Paths.Home, name)
	return path, key, err
}
