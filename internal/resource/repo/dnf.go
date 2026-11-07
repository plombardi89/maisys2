package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
)

// reposDir holds dnf's .repo files.
const reposDir = "/etc/yum.repos.d"

// DnfRepo is a dnf repo (Fedora), written to /etc/yum.repos.d/NAME.repo.
// The plan shows it as "repo NAME".
type DnfRepo struct {
	// Name names the repo, and its file.
	Name string
	// Content is the .repo file, as the repo's vendor publishes it: dnf
	// imports the key its gpgkey names when it first installs from it.
	Content string

	// root is where the file system starts: "" means "/".
	root string
}

var _ resource.Resource = (*DnfRepo)(nil)

// ID is "repo:NAME".
func (r *DnfRepo) ID() string { return Kind + ":" + r.Name }

// Kind is Kind.
func (r *DnfRepo) Kind() string { return Kind }

// Targets is the .repo file.
func (r *DnfRepo) Targets() []string { return []string{r.file()} }

// Privileged is true: the file belongs to root.
func (r *DnfRepo) Privileged() bool { return true }

func (r *DnfRepo) file() string { return reposDir + "/" + r.Name + ".repo" }

func (r *DnfRepo) check(rt *resource.Runtime) error {
	if err := checkName(r.Name, rt, config.Fedora); err != nil {
		return err
	}
	if !strings.Contains(r.Content, "[") {
		return fmt.Errorf("repo %s's .repo file has no [section]", r.Name)
	}
	return nil
}

// Check compares the .repo file with what's wanted.
func (r *DnfRepo) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := r.check(rt); err != nil {
		return resource.Change{}, err
	}
	same, exists, err := sameFile(r.root, r.file(), []byte(r.Content))
	switch {
	case err != nil:
		return resource.Change{}, err
	case !exists:
		return resource.Change{Action: resource.Create, Summary: "add repo " + r.Name}, nil
	case !same:
		return resource.Change{Action: resource.Update, Summary: "update repo " + r.Name}, nil
	}
	return resource.Change{}, nil
}

// Apply writes the .repo file.
func (r *DnfRepo) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := r.check(rt); err != nil {
		return state.Record{}, err
	}
	m, err := pkg.ManagerFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	if err := writeFile(ctx, rt, r.root, r.file(), []byte(r.Content)); err != nil {
		return state.Record{}, err
	}
	m.SourcesChanged()
	return state.Record{Details: map[string]string{"type": typeDnfRepo}}, nil
}
