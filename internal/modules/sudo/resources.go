package sudo

import (
	"fmt"
	"os/user"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/sudoers"
)

var _ engine.Module = Module{}

// currentUser returns the name of the user maisys runs as. Tests replace
// it.
var currentUser = defaultCurrentUser

func defaultCurrentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("finding your user name: %w", err)
	}
	return u.Username, nil
}

// DependsOn returns nothing: the rule only needs sudo, which the plan
// checks for.
func (Module) DependsOn() []string { return nil }

// Resources returns the rule that lets your user run sudo without a
// password. Turning nopasswd off leaves the rule to the sudoers remover,
// which deletes it unless that would take away your sudo access.
func (Module) Resources(cfg config.ModuleConfig, _ *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the sudo module can't use a %T config", cfg)
	}
	if !c.NoPasswd {
		return nil, nil
	}
	name, err := currentUser()
	if err != nil {
		return nil, err
	}
	return []resource.Resource{&sudoers.Rule{User: name}}, nil
}
