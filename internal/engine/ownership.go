package engine

import (
	"fmt"
	"path"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
)

// claim is a path a resource writes: one of its targets.
type claim struct {
	module, id string
}

// claimTargets adds the targets of module name's resources to claims,
// which maps each path, as ownedPath returns it, to the resource that
// writes it. It returns an error if a path is taken. Only the same path
// counts: a resource may write inside a folder another one writes.
func claimTargets(claims map[string]claim, name string, resources []resource.Resource, home string, windows bool) error {
	for _, r := range resources {
		mine := map[string]bool{}
		for _, target := range r.Targets() {
			if target == "" {
				continue
			}
			p := ownedPath(target, home, windows)
			if mine[p] {
				continue
			}
			mine[p] = true
			c, ok := claims[p]
			switch {
			case !ok:
				claims[p] = claim{module: name, id: r.ID()}
			case c.module == name:
				return fmt.Errorf("module %s has two resources that write %s (%s and %s)", name, target, c.id, r.ID())
			default:
				return fmt.Errorf("modules %s and %s both write %s (%s and %s)", c.module, name, target, c.id, r.ID())
			}
		}
	}
	return nil
}

// ownedPath returns the path target names, in the form the ownership check
// compares: absolute if it's in the home folder home ("~" or "~/..."),
// and cleaned. On a Windows machine, where paths ignore case and "\"
// separates folders too, it's in lower case with "/" separators.
func ownedPath(target, home string, windows bool) string {
	p := target
	if windows {
		p, home = strings.ReplaceAll(p, `\`, "/"), strings.ReplaceAll(home, `\`, "/")
	}
	if home != "" {
		if p == "~" {
			p = home
		} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = home + "/" + rest
		}
	}
	p = path.Clean(p)
	if windows {
		p = strings.ToLower(p)
	}
	return p
}
