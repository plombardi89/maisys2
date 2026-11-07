package modules_test

import (
	"testing"

	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/shells"
)

// TestBehaviour checks which modules have resources (engine.Module) and
// which add to the shell environment (shells.Contributor). A module that
// gains either is added here, one line each, in alphabetical order.
func TestBehaviour(t *testing.T) {
	want := map[string]struct{ resources, shellEnv bool }{
		"shell": {resources: true},
	}

	names := map[string]bool{}
	for _, mod := range modules.All() {
		name := mod.Info().Name
		names[name] = true
		_, resources := mod.(engine.Module)
		_, shellEnv := mod.(shells.Contributor)
		w := want[name]
		if resources != w.resources {
			t.Errorf("%s implements engine.Module: %t, want %t", name, resources, w.resources)
		}
		if shellEnv != w.shellEnv {
			t.Errorf("%s implements shells.Contributor: %t, want %t", name, shellEnv, w.shellEnv)
		}
	}
	for name := range want {
		if !names[name] {
			t.Errorf("there's no module called %s", name)
		}
	}
}
