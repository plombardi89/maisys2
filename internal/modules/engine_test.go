package modules_test

import (
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules"
)

// TestEngineModules checks which modules have resources: those that
// implement engine.Module. Add a module here once it does.
func TestEngineModules(t *testing.T) {
	want := map[string]bool{
		"sudo":      true,
		"packages":  true,
		"files":     true,
		"hashicorp": true,
		"podman":    true,
		"steam":     true,
		"wsl":       true,
		"legacy":    true,
	}
	var names []string
	for _, mod := range modules.All() {
		names = append(names, mod.Info().Name)
	}
	for _, mod := range modules.All() {
		name := mod.Info().Name
		m, ok := mod.(engine.Module)
		if ok != want[name] {
			t.Errorf("%s implements engine.Module: %v, want %v", name, ok, want[name])
		}
		if !ok {
			continue
		}
		for _, dep := range m.DependsOn() {
			if !slices.Contains(names, dep) || dep == name {
				t.Errorf("%s depends on %q, which isn't another module", name, dep)
			}
		}
	}
}
