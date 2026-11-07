package modules_test

import (
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/shells"
)

// What each module does beyond its config. A module that gains resources
// or a shell environment is added here, in alphabetical order.
var (
	// engineModules have resources (engine.Module). winget gains its own
	// in M9.
	engineModules = []string{
		"files", "golang", "golangci_lint", "hashicorp", "intellij", "kubectl", "legacy", "lnav", "mage",
		"mutagen", "opencode", "packages", "podman", "protoc", "python_tools", "sdkman", "shell", "steam",
		"stern", "sudo", "windows_terminal", "winget", "wsl", "wsl_host", "yq",
	}
	// shellContributors add to the shell environment (shells.Contributor).
	shellContributors = []string{
		"golang", "golangci_lint", "intellij", "kubectl", "lnav", "mage", "mutagen", "opencode", "protoc",
		"python_tools", "sdkman", "stern", "wsl", "yq",
	}
)

// TestBehaviour checks which modules have resources and which add to the
// shell environment, and that modules only depend on other modules.
func TestBehaviour(t *testing.T) {
	names := map[string]bool{}
	for _, mod := range modules.All() {
		names[mod.Info().Name] = true
	}
	for _, name := range slices.Concat(engineModules, shellContributors) {
		if !names[name] {
			t.Errorf("there's no module %s", name)
		}
	}
	for _, mod := range modules.All() {
		name := mod.Info().Name
		em, isEngine := mod.(engine.Module)
		if want := slices.Contains(engineModules, name); isEngine != want {
			t.Errorf("%s implements engine.Module: %t, want %t", name, isEngine, want)
		}
		if _, ok := mod.(shells.Contributor); ok != slices.Contains(shellContributors, name) {
			t.Errorf("%s implements shells.Contributor: %t, want %t", name, ok, !ok)
		}
		if !isEngine {
			continue
		}
		for _, dep := range em.DependsOn() {
			if !names[dep] || dep == name {
				t.Errorf("%s depends on %q, which isn't another module", name, dep)
			}
		}
	}
}
