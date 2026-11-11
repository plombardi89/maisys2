package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// names returns the names of the plan's modules.
func names(p *Plan) []string {
	var ns []string
	for _, m := range p.Modules {
		ns = append(ns, m.Name)
	}
	return ns
}

// removalIDs returns the IDs of the plan's removals.
func removalIDs(p *Plan) []string {
	var ids []string
	for _, r := range p.Removals {
		ids = append(ids, r.Record.ID)
	}
	return ids
}

func TestPlanChecksInParallel(t *testing.T) {
	const limit, total = 3, 12
	e := newTestEnv(t)
	e.parallel = limit
	var running, most atomic.Int32
	started := make(chan string, total)
	release := make(chan struct{})
	for i := range total {
		r := &fakeResource{id: fmt.Sprintf("fake:%02d", i)}
		r.onCheck = func(context.Context, *resource.Runtime) (resource.Change, error) {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				if m := most.Load(); n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			started <- r.id
			<-release
			return resource.Change{Action: resource.Create, Summary: r.id}, nil
		}
		e.add(fmt.Sprintf("module%d", i%4), nil, r)
	}

	done := make(chan *Plan)
	go func() {
		p, err := NewPlan(t.Context(), e.input())
		if err != nil {
			t.Error(err)
		}
		done <- p
	}()
	for range limit {
		<-started
	}
	select {
	case id := <-started:
		t.Errorf("check of %s started while %d others were running", id, limit)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	p := <-done
	if got := most.Load(); got != limit {
		t.Errorf("at most %d checks ran at once, want %d", got, limit)
	}
	if p != nil && len(p.Modules) != 4 {
		t.Errorf("the plan has %d modules, want 4", len(p.Modules))
	}
}

// TestPlanOrderIsDeterministic makes checks finish in the opposite order
// to the one they're listed in, and checks that the plan keeps the modules'
// order.
func TestPlanOrderIsDeterministic(t *testing.T) {
	const total = 6
	e := newTestEnv(t)
	finished := make([]chan struct{}, total+1)
	for i := range finished {
		finished[i] = make(chan struct{})
	}
	close(finished[total])
	var order []string
	for i := range total {
		r := &fakeResource{id: fmt.Sprintf("fake:%d", i)}
		r.onCheck = func(context.Context, *resource.Runtime) (resource.Change, error) {
			<-finished[i+1]
			defer close(finished[i])
			return resource.Change{Action: resource.Create, Summary: fmt.Sprintf("item %d", i)}, nil
		}
		e.add(fmt.Sprintf("m%d", i/2), nil, r)
		order = append(order, fmt.Sprintf("item %d", i))
	}

	p := e.plan()
	var got []string
	for _, m := range p.Modules {
		for _, item := range m.Items {
			got = append(got, item.Change.Summary)
		}
	}
	if !slices.Equal(got, order) {
		t.Errorf("the plan's changes are %q, want %q", got, order)
	}
}

func TestPlanDuplicateIDs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *testEnv)
		want  string
	}{
		{
			name: "one module",
			setup: func(e *testEnv) {
				e.add("files", nil, res("file:~/.zshrc", resource.Create, ""), res("file:~/.zshrc", resource.Create, ""))
			},
			want: "module files has two resources with the ID file:~/.zshrc",
		},
		{
			name: "two modules",
			setup: func(e *testEnv) {
				e.add("files", nil, res("file:~/.zshrc", resource.Create, ""))
				e.add("shell", nil, res("file:~/.zshrc", resource.Create, ""))
			},
			want: "modules files and shell both have a resource with the ID file:~/.zshrc",
		},
		{
			name: "a module left out by only",
			setup: func(e *testEnv) {
				e.only = []string{"files"}
				e.add("files", nil, res("file:~/.zshrc", resource.Create, ""))
				e.add("shell", nil, res("file:~/.zshrc", resource.Create, ""))
			},
			want: "modules files and shell both have a resource with the ID file:~/.zshrc",
		},
		{
			name:  "no ID",
			setup: func(e *testEnv) { e.add("files", nil, res("", resource.Create, "")) },
			want:  "module files has a resource without an ID",
		},
		{
			name: "two modules with the same name",
			setup: func(e *testEnv) {
				e.modules = append(e.modules, configOnly{"files"}, configOnly{"files"})
			},
			want: "there are two modules called files",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			tt.setup(e)
			if _, err := NewPlan(t.Context(), e.input()); err == nil || err.Error() != tt.want {
				t.Errorf("NewPlan() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPlanOwnership(t *testing.T) {
	// writes returns a fake resource with id that writes targets.
	writes := func(id string, targets ...string) *fakeResource {
		r := res(id, resource.Create, "")
		r.targets = targets
		return r
	}
	tests := []struct {
		name  string
		setup func(e *testEnv)
		want  string // "" if there's no error
	}{
		{
			name: "two modules",
			setup: func(e *testEnv) {
				e.add("files", nil, writes("file:~/.zshrc", "~/.zshrc"))
				e.add("shell", nil, writes("shell-rc:zsh", "~/.zshrc"))
			},
			want: "modules files and shell both write ~/.zshrc (file:~/.zshrc and shell-rc:zsh)",
		},
		{
			name: "one module",
			setup: func(e *testEnv) {
				e.add("golang", nil,
					writes("tool:golang", "~/.local/share/maisys/tools/go", "~/.local/bin/go", "~/.local/bin/gofmt"),
					writes("symlink:~/.local/bin/go", "~/.local/bin/go"),
				)
			},
			want: "module golang has two resources that write ~/.local/bin/go (tool:golang and symlink:~/.local/bin/go)",
		},
		{
			name: "the home folder written out",
			setup: func(e *testEnv) {
				e.add("files", nil, writes("file:~/.zshrc", "/home/phil/.zshrc"))
				e.add("shell", nil, writes("shell-rc:zsh", "~/.zshrc"))
			},
			want: "modules files and shell both write ~/.zshrc (file:~/.zshrc and shell-rc:zsh)",
		},
		{
			name: "paths that aren't clean",
			setup: func(e *testEnv) {
				e.add("files", nil, writes("file:a", "~/.config/nvim/init.lua"))
				e.add("nvim", nil, writes("file:b", "~/.config//nvim/./lua/../init.lua"))
			},
			want: "modules files and nvim both write ~/.config//nvim/./lua/../init.lua (file:a and file:b)",
		},
		{
			name: "outside the home folder",
			setup: func(e *testEnv) {
				e.add("sudo", nil, writes("sudoers:phil", "/etc/sudoers.d/phil"))
				e.add("files", nil, writes("file:sudoers", "/etc/sudoers.d/phil/"))
			},
			want: "modules sudo and files both write /etc/sudoers.d/phil/ (sudoers:phil and file:sudoers)",
		},
		{
			name: "Windows paths ignore case",
			setup: func(e *testEnv) {
				e.windows()
				e.add("wsl_host", nil, writes("file:~/.wslconfig", `~\.wslconfig`))
				e.add("files", nil, writes("file:wslconfig", `C:\Users\Phil\.WSLConfig`))
			},
			want: `modules wsl_host and files both write C:\Users\Phil\.WSLConfig (file:~/.wslconfig and file:wslconfig)`,
		},
		{
			name: "Linux paths don't",
			setup: func(e *testEnv) {
				e.add("files", nil, writes("file:a", "~/.zshrc"))
				e.add("shell", nil, writes("file:b", "~/.ZSHRC"))
			},
		},
		{
			name: "inside a folder another resource writes",
			setup: func(e *testEnv) {
				e.add("shell", nil,
					writes("git-checkout:omz", "~/.oh-my-zsh"),
					writes("symlink:theme", "~/.oh-my-zsh/custom/themes/theme.zsh-theme"),
				)
				e.add("files", nil, writes("file:plugin", "~/.oh-my-zsh/custom/plugins/plugin.zsh"))
			},
		},
		{
			name: "the same path twice in one resource",
			setup: func(e *testEnv) {
				e.add("golang", nil, writes("tool:golang", "~/.local/bin/go", "/home/phil/.local/bin/go"))
			},
		},
		{
			name: "a module left out by only",
			setup: func(e *testEnv) {
				e.only = []string{"files"}
				e.add("files", nil, writes("file:~/.zshrc", "~/.zshrc"))
				e.add("shell", nil, writes("shell-rc:zsh", "~/.zshrc"))
			},
			want: "modules files and shell both write ~/.zshrc (file:~/.zshrc and shell-rc:zsh)",
		},
		{
			name: "a module that's turned off",
			setup: func(e *testEnv) {
				e.add("files", nil, writes("file:~/.zshrc", "~/.zshrc"))
				e.add("shell", nil, writes("shell-rc:zsh", "~/.zshrc"))
				e.disable("shell")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			tt.setup(e)
			in := e.input()
			in.Paths.Home = "/home/phil"
			if e.machine.OS == config.Windows {
				in.Paths.Home = `C:\Users\phil`
			}
			_, err := NewPlan(t.Context(), in)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("NewPlan() error = %v, want none", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Errorf("NewPlan() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPlanDependencyOrder(t *testing.T) {
	type mod struct {
		name     string
		deps     []string
		disabled bool
	}
	tests := []struct {
		name    string
		modules []mod
		only    []string
		want    []string
		wantErr string
	}{
		{
			name:    "no dependencies keep their order",
			modules: []mod{{name: "c"}, {name: "a"}, {name: "b"}},
			want:    []string{"c", "a", "b"},
		},
		{
			name:    "after what they depend on",
			modules: []mod{{name: "mage", deps: []string{"golang"}}, {name: "files"}, {name: "golang", deps: []string{"packages"}}, {name: "packages"}},
			want:    []string{"files", "packages", "golang", "mage"},
		},
		{
			name:    "several dependencies",
			modules: []mod{{name: "d", deps: []string{"c", "a"}}, {name: "c"}, {name: "b"}, {name: "a"}},
			want:    []string{"c", "b", "a", "d"},
		},
		{
			name:    "disabled dependency",
			modules: []mod{{name: "mage", deps: []string{"golang"}}, {name: "golang", disabled: true}},
			want:    []string{"mage"},
		},
		{
			name:    "dependency left out by only",
			modules: []mod{{name: "mage", deps: []string{"golang"}}, {name: "golang"}, {name: "files"}},
			only:    []string{"files", "mage"},
			want:    []string{"files"},
		},
		{
			name:    "disabled dependency left out by only",
			modules: []mod{{name: "mage", deps: []string{"golang"}}, {name: "golang", disabled: true}, {name: "files"}},
			only:    []string{"files", "mage"},
			want:    []string{"mage", "files"},
		},
		{
			name:    "cycle",
			modules: []mod{{name: "a"}, {name: "b", deps: []string{"c"}}, {name: "c", deps: []string{"d"}}, {name: "d", deps: []string{"b"}}},
			wantErr: "modules depend on each other in a cycle: b → c → d → b",
		},
		{
			name:    "depends on itself",
			modules: []mod{{name: "a", deps: []string{"a"}}},
			wantErr: "modules depend on each other in a cycle: a → a",
		},
		{
			name:    "cycle with a disabled module",
			modules: []mod{{name: "a", deps: []string{"b"}}, {name: "b", deps: []string{"a"}, disabled: true}},
			want:    []string{"a"},
		},
		{
			name:    "unknown dependency",
			modules: []mod{{name: "a", deps: []string{"nope"}, disabled: true}},
			wantErr: `module a depends on module "nope", which maisys doesn't have`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.only = tt.only
			for _, m := range tt.modules {
				e.add(m.name, m.deps, res("fake:"+m.name, resource.Create, ""))
				if m.disabled {
					e.disable(m.name)
				}
			}
			p, err := NewPlan(t.Context(), e.input())
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("NewPlan() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := names(p); !slices.Equal(got, tt.want) {
				t.Errorf("modules = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlanOnly(t *testing.T) {
	setup := func(t *testing.T, only ...string) *testEnv {
		t.Helper()
		e := newTestEnv(t)
		e.only = only
		e.add("golang", nil, res("tool:golang", resource.Create, "toolchain"))
		e.add("files", nil, res("file:~/.gitconfig", resource.Create, "~/.gitconfig"))
		e.add("kubectl", nil)
		e.disable("kubectl")
		e.modules = append(e.modules, configOnly{"winget"})
		e.record("tool:gopls", "tool", "golang")
		e.record("file:~/.vimrc", "file", "files")
		e.record("tool:kubectl", "tool", "kubectl")
		e.record("tool:mutagen", "tool", "mutagen")
		e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove}})
		e.remover(&fakeRemover{kind: "file", change: resource.Change{Action: resource.Remove}})
		return e
	}

	tests := []struct {
		only         []string
		wantModules  []string
		wantRemovals []string
	}{
		{
			wantModules:  []string{"golang", "files"},
			wantRemovals: []string{"tool:mutagen", "tool:kubectl", "file:~/.vimrc", "tool:gopls"},
		},
		{
			only:         []string{"files"},
			wantModules:  []string{"files"},
			wantRemovals: []string{"file:~/.vimrc"},
		},
		{
			only:         []string{"golang", "kubectl", "winget"},
			wantModules:  []string{"golang"},
			wantRemovals: []string{"tool:kubectl", "tool:gopls"},
		},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.only, ","), func(t *testing.T) {
			p := setup(t, tt.only...).plan()
			if got := names(p); !slices.Equal(got, tt.wantModules) {
				t.Errorf("modules = %q, want %q", got, tt.wantModules)
			}
			if got := removalIDs(p); !slices.Equal(got, tt.wantRemovals) {
				t.Errorf("removals = %q, want %q", got, tt.wantRemovals)
			}
		})
	}

	t.Run("unknown module", func(t *testing.T) {
		e := setup(t, "files", "nope")
		_, err := NewPlan(t.Context(), e.input())
		if want := `can't limit the sync to module "nope": there's no module by that name`; err == nil || err.Error() != want {
			t.Errorf("NewPlan() error = %v, want %q", err, want)
		}
	})
}

// TestPlanOnlyHoldsBack limits syncs to modules that depend on others:
// those that depend on an enabled module the sync leaves out aren't
// checked, applied or pruned, and that isn't a failure.
func TestPlanOnlyHoldsBack(t *testing.T) {
	tests := []struct {
		name         string
		only         []string
		disabled     []string
		wantModules  []string
		wantHeld     []HeldBack
		wantRemovals []string
	}{
		{
			name:         "dependencies left out",
			only:         []string{"legacy", "files"},
			wantModules:  []string{"files"},
			wantHeld:     []HeldBack{{Name: "legacy", Needs: []string{"golang", "kubectl"}}},
			wantRemovals: []string{"file:~/.vimrc"},
		},
		{
			name:        "a dependency left out",
			only:        []string{"kubectl", "legacy"},
			wantModules: []string{"kubectl"},
			wantHeld:    []HeldBack{{Name: "legacy", Needs: []string{"golang"}}},
		},
		{
			name:         "dependencies included",
			only:         []string{"legacy", "golang", "kubectl", "packages"},
			wantModules:  []string{"packages", "golang", "kubectl", "legacy"},
			wantRemovals: []string{"cleanup:~/.ansible"},
		},
		{
			name:         "dependencies that aren't enabled",
			only:         []string{"legacy"},
			disabled:     []string{"golang", "kubectl"},
			wantModules:  []string{"legacy"},
			wantRemovals: []string{"cleanup:~/.ansible"},
		},
		{
			name: "a dependency left out by a dependency",
			only: []string{"mage", "golang"},
			wantHeld: []HeldBack{
				{Name: "golang", Needs: []string{"packages"}},
				{Name: "mage", Needs: []string{"packages"}},
			},
		},
		{
			name:         "every module",
			wantModules:  []string{"packages", "golang", "kubectl", "legacy", "mage", "files"},
			wantRemovals: []string{"file:~/.vimrc", "cleanup:~/.ansible"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.only = tt.only
			var checked sync.Map
			add := func(name string, deps ...string) {
				r := res("fake:"+name, resource.Create, name)
				r.onCheck = func(context.Context, *resource.Runtime) (resource.Change, error) {
					checked.Store(name, true)
					return r.change, nil
				}
				e.add(name, deps, r)
			}
			add("packages")
			add("golang", "packages")
			add("kubectl")
			add("legacy", "golang", "kubectl")
			add("mage", "golang")
			add("files")
			for _, name := range tt.disabled {
				e.disable(name)
			}
			e.record("cleanup:~/.ansible", "cleanup", "legacy")
			e.record("file:~/.vimrc", "file", "files")
			e.remover(&fakeRemover{kind: "cleanup", change: resource.Change{Action: resource.Remove}})
			e.remover(&fakeRemover{kind: "file", change: resource.Change{Action: resource.Remove}})

			p := e.plan()
			if got := names(p); !slices.Equal(got, tt.wantModules) {
				t.Errorf("modules = %q, want %q", got, tt.wantModules)
			}
			var held []HeldBack
			for _, h := range p.HeldBack {
				held = append(held, *h)
				if _, ok := checked.Load(h.Name); ok {
					t.Errorf("%s is held back, but its resources were checked", h.Name)
				}
			}
			if !reflect.DeepEqual(held, tt.wantHeld) {
				t.Errorf("held back = %+v, want %+v", held, tt.wantHeld)
			}
			if got := removalIDs(p); !slices.Equal(got, tt.wantRemovals) {
				t.Errorf("removals = %q, want %q", got, tt.wantRemovals)
			}
			if p.Failed() {
				t.Error("Failed() = true, want false")
			}

			result := Apply(t.Context(), p, ApplyInput{Store: e.store})
			if !result.OK() || result.ExitCode() != 0 {
				t.Errorf("OK(), ExitCode() = %t, %d; want true, 0", result.OK(), result.ExitCode())
			}
			var want []string
			for _, name := range tt.wantModules {
				want = append(want, "apply fake:"+name)
			}
			for _, id := range tt.wantRemovals {
				want = append(want, "remove "+id)
			}
			if got := e.steps.get(); !slices.Equal(got, want) {
				t.Errorf("steps = %q, want %q", got, want)
			}
		})
	}
}

func TestPlanRemovals(t *testing.T) {
	e := newTestEnv(t)
	e.add("golang", nil, res("tool:golang", resource.NoChange, ""))
	e.add("kubectl", nil)
	e.disable("kubectl")
	e.record("tool:golang", "tool", "golang")
	e.record("tool:gopls", "tool", "golang")
	e.record("tool:kubectl", "tool", "kubectl")
	e.record("repo:hashicorp", "repo", "hashicorp")
	e.record("file:~/.bashrc", "file", "files")
	e.record("file:~/.old", "file", "files")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove, Summary: "remove it"}})
	e.remover(&fakeRemover{kind: "file", changes: map[string]resource.Change{
		"file:~/.bashrc": {Action: resource.Warn, Summary: "~/.bashrc is no longer managed (left in place)"},
		"file:~/.old":    {Action: resource.NoChange},
	}})

	p := e.plan()
	type removal struct {
		id      string
		action  resource.Action
		remover bool
	}
	var got []removal
	for _, r := range p.Removals {
		got = append(got, removal{r.Record.ID, r.Change.Action, r.Remover != nil})
	}
	want := []removal{
		{"file:~/.bashrc", resource.Warn, true},
		{"file:~/.old", resource.NoChange, true},
		{"repo:hashicorp", resource.Orphan, false},
		{"tool:kubectl", resource.Remove, true},
		{"tool:gopls", resource.Remove, true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("removals = %+v, want %+v", got, want)
	}
	if !p.HasChanges() {
		t.Error("HasChanges() = false, want true")
	}
}

// TestPlanRemovalsOfMovedResources checks that a record is removed only if
// no enabled module has its resource any more, even when the sync is
// limited to the module the record names: a resource can move to another
// module, and its record names the old one until the resource next
// changes. If a module can't list its resources, nothing is removed.
func TestPlanRemovalsOfMovedResources(t *testing.T) {
	const heldBack = "removals held back: files couldn't list what it manages: the config repo's files aren't available"
	tests := []struct {
		name         string
		only         []string
		disabled     string // a module that's turned off
		failing      bool   // whether files can't list its resources
		wantModules  []string
		wantRemovals []string
		wantWarnings []string
	}{
		{
			name:         "full sync",
			wantModules:  []string{"shell", "files"},
			wantRemovals: []string{"file:~/.vimrc", "file:~/.old"},
		},
		{
			name:         "only the module the record names",
			only:         []string{"shell"},
			wantModules:  []string{"shell"},
			wantRemovals: []string{"file:~/.old"},
		},
		{
			name:         "only the module that has it now",
			only:         []string{"files"},
			wantModules:  []string{"files"},
			wantRemovals: []string{"file:~/.vimrc"},
		},
		{
			name:         "the module that has it now is turned off",
			disabled:     "files",
			wantModules:  []string{"shell"},
			wantRemovals: []string{"file:~/.vimrc", "file:~/.old", "file:~/.zshrc"},
		},
		{
			name:         "the module that has it now fails",
			failing:      true,
			wantModules:  []string{"shell", "files"},
			wantWarnings: []string{heldBack},
		},
		{
			name:         "a module left out by only fails",
			only:         []string{"shell"},
			failing:      true,
			wantModules:  []string{"shell"},
			wantWarnings: []string{heldBack},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.only = tt.only
			e.add("shell", nil, res("file:~/.bashrc", resource.NoChange, ""))
			files := e.add("files", nil, res("file:~/.zshrc", resource.NoChange, ""))
			if tt.failing {
				files.err = errors.New("the config repo's files aren't available")
			}
			if tt.disabled != "" {
				e.disable(tt.disabled)
			}
			e.record("file:~/.bashrc", "file", "shell")
			e.record("file:~/.zshrc", "file", "shell") // moved to files
			e.record("file:~/.old", "file", "shell")
			e.record("file:~/.vimrc", "file", "files")
			e.remover(&fakeRemover{kind: "file", change: resource.Change{Action: resource.Remove}})

			p := e.plan()
			if got := names(p); !slices.Equal(got, tt.wantModules) {
				t.Errorf("modules = %q, want %q", got, tt.wantModules)
			}
			if got := removalIDs(p); !slices.Equal(got, tt.wantRemovals) {
				t.Errorf("removals = %q, want %q", got, tt.wantRemovals)
			}
			if !slices.Equal(p.Warnings, tt.wantWarnings) {
				t.Errorf("warnings = %q, want %q", p.Warnings, tt.wantWarnings)
			}
			if wantFailed := tt.failing && slices.Contains(tt.wantModules, "files"); p.Failed() != wantFailed {
				t.Errorf("Failed() = %t, want %t", p.Failed(), wantFailed)
			}

			e.apply(t.Context())
			want := slices.DeleteFunc([]string{"file:~/.bashrc", "file:~/.old", "file:~/.vimrc", "file:~/.zshrc"}, func(id string) bool {
				return slices.Contains(tt.wantRemovals, id)
			})
			if got := e.ids(); !slices.Equal(got, want) {
				t.Errorf("after applying, the state file has %q, want %q", got, want)
			}
		})
	}
}

// TestPlanNeverPrompts plans checks that run commands as root while sudo
// needs a password: they're checked during apply, and sudo is never
// started.
func TestPlanNeverPrompts(t *testing.T) {
	e := newTestEnv(t)
	e.runner.SudoStatus = system.SudoNeedsPassword
	check := func(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
		_, err := rt.Runner.Run(ctx, system.Cmd{Name: "dpkg-query", Args: []string{"-W", "jq"}, Root: true})
		return resource.Change{Summary: "install jq"}, err
	}
	e.add("packages", nil, &fakeResource{id: "package:jq", onCheck: check})
	e.add("golang", nil, res("tool:golang", resource.NoChange, ""))

	p := e.plan()
	item := p.Modules[0].Items[0]
	if item.Err != nil || item.Change.Action != resource.Unknown || item.Change.Summary != "install jq" {
		t.Errorf("the check that needs a password: change = %+v, error = %v; want Unknown with its summary", item.Change, item.Err)
	}
	if !p.HasChanges() || !p.NeedsPrivileges() || p.Failed() {
		t.Errorf("HasChanges() = %t, NeedsPrivileges() = %t, Failed() = %t; want changes that need privileges", p.HasChanges(), p.NeedsPrivileges(), p.Failed())
	}
	if n := e.runner.SudoStarts(); n != 0 {
		t.Errorf("the plan started sudo %d times", n)
	}
	for _, c := range e.runner.Calls() {
		if c.Ran || c.InSession {
			t.Errorf("command %s ran (in a session: %t)", c.Cmd, c.InSession)
		}
	}
}

func TestPlanFailures(t *testing.T) {
	e := newTestEnv(t)
	broken := res("tool:golang", resource.Update, "toolchain")
	broken.checkErr = errors.New("lookup failed")
	e.add("golang", nil, broken, res("go-install:task", resource.Create, "task"))
	e.add("mage", []string{"golang"}, res("tool:mage", resource.Create, "mage"))
	e.add("lint", []string{"mage"}, res("tool:lint", resource.Create, "lint"))
	e.add("files", nil, res("file:~/.gitconfig", resource.Create, "gitconfig"))
	broken2 := res("tool:kubectl", resource.Create, "kubectl")
	broken2.checkErr = errors.New("no such version")
	e.add("kubectl", nil, broken2)
	e.record("tool:gopls", "tool", "golang")
	e.record("tool:old-lint", "tool", "lint")
	e.record("tool:kubectl-old", "tool", "kubectl")
	e.record("tool:stern", "tool", "stern")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove}})

	p := e.plan()
	if !p.Failed() {
		t.Error("Failed() = false, want true")
	}
	skipped := map[string]string{}
	for _, m := range p.Modules {
		skipped[m.Name] = m.SkippedBecause
	}
	if want := map[string]string{"golang": "", "mage": "golang", "lint": "golang", "files": "", "kubectl": ""}; fmt.Sprint(skipped) != fmt.Sprint(want) {
		t.Errorf("skipped because = %v, want %v", skipped, want)
	}
	if got := removalIDs(p); !slices.Equal(got, []string{"tool:stern"}) {
		t.Errorf("removals = %q, want only the record of a module that didn't fail", got)
	}
	if changes, removals, unknown := p.counts(); changes != 1 || removals != 1 || unknown != 0 {
		t.Errorf("counts() = %d, %d, %d; want 1 change and 1 removal", changes, removals, unknown)
	}
}

// TestPlanChecksSudo checks that a plan that needs root stops if sudo is
// missing or the user may not use it, and only then.
func TestPlanChecksSudo(t *testing.T) {
	tests := []struct {
		status     system.SudoStatus
		privileged bool
		windows    bool
		wantErr    bool
	}{
		{status: system.SudoMissing, privileged: true, wantErr: true},
		{status: system.SudoNotAllowed, privileged: true, wantErr: true},
		{status: system.SudoNeedsPassword, privileged: true},
		{status: system.SudoNoPassword, privileged: true},
		{status: system.SudoMissing},
		{status: system.SudoMissing, privileged: true, windows: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s privileged=%t windows=%t", tt.status, tt.privileged, tt.windows), func(t *testing.T) {
			e := newTestEnv(t)
			if tt.windows {
				e.windows()
			}
			e.runner.SudoStatus = tt.status
			r := res("package:jq", resource.Create, "install jq")
			r.privileged = tt.privileged
			e.add("packages", nil, r)
			_, err := NewPlan(t.Context(), e.input())
			if _, ok := errors.AsType[*system.SudoError](err); ok != tt.wantErr {
				t.Errorf("NewPlan() error = %v, want a *SudoError: %t", err, tt.wantErr)
			}
			if checked, want := e.runner.SudoChecks() > 0, tt.privileged && !tt.windows; checked != want {
				t.Errorf("sudo was checked %d times, want a check: %t", e.runner.SudoChecks(), want)
			}
		})
	}
}

func TestPlanCancelled(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	r := &fakeResource{id: "fake:1", onCheck: func(ctx context.Context, _ *resource.Runtime) (resource.Change, error) {
		cancel()
		return resource.Change{}, ctx.Err()
	}}
	e.add("golang", nil, r)
	if _, err := NewPlan(ctx, e.input()); !errors.Is(err, context.Canceled) {
		t.Errorf("NewPlan() error = %v, want context.Canceled", err)
	}
}

func TestPlanRuntime(t *testing.T) {
	e := newTestEnv(t)
	e.store.Put(state.Record{ID: "tool:golang", Kind: "tool", Module: "golang", Details: map[string]string{"version": "1.27.3"}})
	var got string
	r := &fakeResource{id: "tool:golang", onCheck: func(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
		rec, ok := rt.Previous("tool:golang")
		if _, other := rt.Previous("tool:other"); other {
			t.Error("Previous() found a record that doesn't exist")
		}
		if rt.Machine != e.machine || rt.Facts != e.facts || rt.Runner != e.runner || !rt.Now().Equal(testNow) {
			t.Errorf("the runtime = %+v, doesn't match the plan's input", rt)
		}
		if ok {
			got = rec.Details["version"]
		}
		return resource.Change{}, nil
	}}
	e.add("golang", nil, r)
	if p := e.plan(); p.HasChanges() || p.NeedsPrivileges() || p.Failed() {
		t.Errorf("HasChanges(), NeedsPrivileges(), Failed() = %t, %t, %t; want false", p.HasChanges(), p.NeedsPrivileges(), p.Failed())
	}
	if got != "1.27.3" {
		t.Errorf("Previous() found version %q, want the record's", got)
	}
}

func TestPlanWithoutModulesWithResources(t *testing.T) {
	e := newTestEnv(t)
	e.modules = []config.Module{configOnly{"golang"}, configOnly{"files"}}
	e.machine.Modules["golang"] = &config.Toggle{Enabled: true}
	p := e.plan()
	if len(p.Modules) != 0 || len(p.Removals) != 0 || p.HasChanges() {
		t.Errorf("plan = %+v, want nothing to do", p)
	}
}

// stopError is a check error that stops the whole sync.
type stopError struct{}

func (stopError) Error() string   { return "can't find the latest version" }
func (stopError) StopsSync() bool { return true }

func TestPlanStopError(t *testing.T) {
	e := newTestEnv(t)
	failing := res("fake:a", resource.Create, "a")
	failing.checkErr = errors.New("an ordinary failure")
	e.add("alpha", nil, failing)
	stopping := res("fake:b", resource.Create, "b")
	stopping.checkErr = fmt.Errorf("checking b: %w", stopError{})
	e.add("beta", nil, stopping)

	p, err := NewPlan(t.Context(), e.input())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.StopError(); !errors.As(err, new(stopError)) {
		t.Errorf("StopError() = %v, want the lookup error, which stops the sync", err)
	}

	e = newTestEnv(t)
	e.add("alpha", nil, failing)
	if p, err = NewPlan(t.Context(), e.input()); err != nil {
		t.Fatal(err)
	}
	if err := p.StopError(); err != nil {
		t.Errorf("StopError() = %v, want nil: an ordinary failure only fails its module", err)
	}
}
