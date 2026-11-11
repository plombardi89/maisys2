package engine

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// TestApplySavesAfterEveryStep checks the state file from each step: it
// must already hold the records of every step before it.
func TestApplySavesAfterEveryStep(t *testing.T) {
	e := newTestEnv(t)
	var seen [][]string
	saw := func() []string {
		ids := e.ids()
		seen = append(seen, ids)
		return ids
	}
	step := func(id string) *fakeResource {
		r := res(id, resource.Create, id)
		r.onApply = func(context.Context, *resource.Runtime) (state.Record, error) {
			saw()
			return state.Record{}, nil
		}
		return r
	}
	e.add("golang", nil, step("tool:golang"), res("tool:gopls", resource.NoChange, ""), step("go-install:task"))
	e.add("files", nil, step("file:~/.gitconfig"))
	e.record("tool:kubectl", "tool", "kubectl")
	e.record("tool:stern", "tool", "stern")
	if err := e.store.Save(); err != nil {
		t.Fatal(err)
	}
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove}, onRemove: func(context.Context, state.Record) { saw() }})

	result := e.apply(t.Context())
	if !result.OK() {
		var b bytes.Buffer
		result.Render(&b)
		t.Fatalf("Apply() failed:\n%s", b.String())
	}
	want := [][]string{
		{"tool:kubectl", "tool:stern"},
		{"tool:golang", "tool:kubectl", "tool:stern"},
		{"go-install:task", "tool:golang", "tool:kubectl", "tool:stern"},
		{"file:~/.gitconfig", "go-install:task", "tool:golang", "tool:kubectl", "tool:stern"},
		{"file:~/.gitconfig", "go-install:task", "tool:golang", "tool:stern"},
	}
	if !slices.EqualFunc(seen, want, slices.Equal) {
		t.Errorf("the steps saw these records in the state file:\n%q\nwant\n%q", seen, want)
	}
	if got, want := e.ids(), []string{"file:~/.gitconfig", "go-install:task", "tool:golang"}; !slices.Equal(got, want) {
		t.Errorf("records at the end = %q, want %q", got, want)
	}

	rec, _ := e.store.Get("tool:golang")
	if rec.Kind != "fake" || rec.Module != "golang" || !rec.Updated.Equal(testNow) {
		t.Errorf("record = %+v, want its kind, module and time filled in", rec)
	}
}

func TestApplyRecord(t *testing.T) {
	e := newTestEnv(t)
	r := res("file:~/.gitconfig", resource.Create, "")
	r.targets = []string{"/home/phil/.gitconfig"}
	r.onApply = func(context.Context, *resource.Runtime) (state.Record, error) {
		return state.Record{ID: "wrong", Kind: "wrong", Module: "wrong", Details: map[string]string{"sha256": "abc"}, TookOver: true}, nil
	}
	e.add("files", nil, r)
	e.apply(t.Context())

	s, err := state.Load(e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := s.Get("file:~/.gitconfig")
	if !ok || rec.Kind != "fake" || rec.Module != "files" || !slices.Equal(rec.Paths, r.targets) || rec.Details["sha256"] != "abc" || !rec.TookOver {
		t.Errorf("saved record = %+v, want the resource's ID, kind, module and targets with what Apply returned", rec)
	}
	if s.Machine != "phil-workp14s" || s.LastSyncResult != state.ResultOK || !s.LastSync.Equal(testNow) {
		t.Errorf("state = machine %q, last sync %s %q; want the machine, and an ok sync at %s", s.Machine, s.LastSync, s.LastSyncResult, testNow)
	}
}

func TestApplySudo(t *testing.T) {
	tests := []struct {
		name       string
		privileged []bool // whether each resource needs root
		windows    bool
		wantStarts int
	}{
		{name: "none", privileged: []bool{false, false}},
		{name: "one", privileged: []bool{false, true, false}, wantStarts: 1},
		{name: "several", privileged: []bool{true, false, true, true}, wantStarts: 1},
		{name: "windows", privileged: []bool{true}, windows: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			if tt.windows {
				e.windows()
			}
			e.runner.SudoStatus = system.SudoNeedsPassword
			e.runner.On("touch", func(context.Context, system.Cmd) (system.Result, error) { return system.Result{}, nil })
			for i, privileged := range tt.privileged {
				r := res("fake:"+string(rune('a'+i)), resource.Create, "")
				r.privileged = privileged
				// Windows has no sudo: its elevated helper comes later.
				if !tt.windows {
					r.onApply = func(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
						_, err := rt.Runner.Run(ctx, system.Cmd{Name: "touch", Args: []string{r.id}, Root: privileged})
						return state.Record{}, err
					}
				}
				e.add("module"+string(rune('a'+i%2)), nil, r)
			}

			result := e.apply(t.Context())
			if !result.OK() {
				var b bytes.Buffer
				result.Render(&b)
				t.Errorf("Apply() failed:\n%s", b.String())
			}
			if n := e.runner.SudoStarts(); n != tt.wantStarts {
				t.Errorf("sudo was started %d times, want %d", n, tt.wantStarts)
			}
			for _, c := range e.runner.Calls() {
				if c.Cmd.Root && !c.InSession {
					t.Errorf("%s ran as root outside the sudo session", c.Cmd)
				}
			}
			if _, err := e.runner.Run(t.Context(), system.Cmd{Name: "touch", Root: true}); !errors.Is(err, system.ErrNeedsPassword) {
				t.Errorf("a command run as root after Apply: error = %v, want ErrNeedsPassword because the session stopped", err)
			}
		})
	}
}

func TestApplySudoFails(t *testing.T) {
	e := newTestEnv(t)
	e.runner.StartErr = errors.New("sudo couldn't check your password: sudo: 3 incorrect password attempts")
	e.add("packages", nil, res("package:jq", resource.Create, "install jq").sudo(), res("package:tmux", resource.Create, "install tmux").sudo())
	e.add("golang", []string{"packages"}, res("tool:golang", resource.Create, "toolchain"))
	e.add("files", nil, res("file:~/.gitconfig", resource.Create, "~/.gitconfig"))
	e.record("unit:x410", "unit", "wsl")
	e.remover(&fakeRemover{kind: "unit", change: resource.Change{Action: resource.Remove, Summary: "x410 service", Privileged: true}})

	result := e.apply(t.Context())
	if got, want := e.steps.get(), []string{"apply file:~/.gitconfig"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	want := "Applied 1 change.\n" +
		"packages failed: install jq: sudo couldn't check your password: sudo: 3 incorrect password attempts\n" +
		"golang skipped because packages failed.\n" +
		"Couldn't remove x410 service: sudo couldn't check your password: sudo: 3 incorrect password attempts\n"
	if b.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), want)
	}
	if n := e.runner.SudoStarts(); n != 1 {
		t.Errorf("sudo was started %d times, want once", n)
	}
}

func TestApplyFailureIsolation(t *testing.T) {
	e := newTestEnv(t)
	broken := res("tool:golang", resource.Update, "toolchain 1.27.3 → 1.27.4 (latest)")
	broken.applyErr = &system.ExitError{Label: "downloading go1.27.4.linux-amd64.tar.gz", Code: 22, Tail: []string{"404 Not Found"}}
	e.add("packages", nil, res("package:jq", resource.Create, "install jq"))
	e.add("golang", []string{"packages"}, res("go-install:task", resource.Create, "task"), broken, res("go-install:gopls", resource.Create, "gopls"))
	e.add("mage", []string{"golang"}, res("tool:mage", resource.Create, "mage"))
	e.add("lint", []string{"files", "mage"}, res("tool:lint", resource.Create, "lint"))
	e.add("files", nil, res("file:~/.gitconfig", resource.Create, "~/.gitconfig"), res("file:~/.vimrc", resource.Create, "~/.vimrc"))
	e.add("kubectl", nil, res("tool:kubectl", resource.Create, "kubectl"))
	e.record("go-install:old", "tool", "golang")
	e.record("tool:old-lint", "tool", "lint")
	e.record("tool:stern", "tool", "stern")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove}})

	result := e.apply(t.Context())
	want := []string{
		"apply package:jq",
		"apply go-install:task",
		"apply tool:golang",
		"apply file:~/.gitconfig",
		"apply file:~/.vimrc",
		"apply tool:kubectl",
		"remove tool:stern",
	}
	if got := e.steps.get(); !slices.Equal(got, want) {
		t.Errorf("steps =\n%q\nwant\n%q", got, want)
	}
	if result.OK() || result.ExitCode() != 1 {
		t.Errorf("OK(), ExitCode() = %t, %d; want false, 1", result.OK(), result.ExitCode())
	}

	var b bytes.Buffer
	result.Render(&b)
	wantOut := "Applied 5 changes and 1 removal.\n" +
		"golang failed: toolchain 1.27.3 → 1.27.4 (latest): downloading go1.27.4.linux-amd64.tar.gz failed with exit code 22: 404 Not Found\n" +
		"mage skipped because golang failed.\n" +
		"lint skipped because golang failed.\n" +
		"Didn't remove tool old-lint because golang failed.\n" +
		"Didn't remove go-install old because golang failed.\n"
	if b.String() != wantOut {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), wantOut)
	}

	if got, want := e.ids(), []string{"file:~/.gitconfig", "file:~/.vimrc", "go-install:old", "go-install:task", "package:jq", "tool:kubectl", "tool:old-lint"}; !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
	if e.store.LastSyncResult != state.ResultFailed {
		t.Errorf("LastSyncResult = %q, want %q", e.store.LastSyncResult, state.ResultFailed)
	}
}

// TestApplyPlanFailures applies a plan in which modules failed: they and
// their dependents aren't applied.
func TestApplyPlanFailures(t *testing.T) {
	e := newTestEnv(t)
	broken := res("tool:golang", resource.Update, "toolchain")
	broken.checkErr = errors.New("lookup failed")
	e.add("golang", nil, res("go-install:task", resource.Create, "task"), broken)
	e.add("mage", []string{"golang"}, res("tool:mage", resource.Create, "mage"))
	e.add("files", nil, res("file:~/.gitconfig", resource.Create, "~/.gitconfig"))
	e.record("tool:stern", "tool", "stern")
	e.remover(&fakeRemover{kind: "tool", checkErr: map[string]error{"tool:stern": errors.New("permission denied")}})

	result := e.apply(t.Context())
	if got, want := e.steps.get(), []string{"apply file:~/.gitconfig"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	want := "Applied 1 change.\n" +
		"golang failed: checking tool golang: lookup failed\n" +
		"mage skipped because golang failed.\n" +
		"Couldn't remove tool stern: permission denied\n"
	if b.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), want)
	}
	if !slices.Contains(e.ids(), "tool:stern") {
		t.Error("the record of a removal that couldn't be checked was deleted")
	}
}

func TestApplyRemovals(t *testing.T) {
	e := newTestEnv(t)
	e.add("golang", nil, res("tool:golang", resource.Create, "toolchain"))
	e.record("tool:kubectl", "tool", "kubectl")
	e.record("tool:gone", "tool", "kubectl")
	e.record("tool:broken", "tool", "kubectl")
	e.record("repo:hashicorp", "repo", "hashicorp")
	e.record("file:~/.bashrc", "file", "files")
	e.remover(&fakeRemover{
		kind:      "tool",
		change:    resource.Change{Action: resource.Remove},
		changes:   map[string]resource.Change{"tool:gone": {Action: resource.NoChange}},
		removeErr: map[string]error{"tool:broken": errors.New("permission denied")},
	})
	e.remover(&fakeRemover{kind: "file", change: resource.Change{Action: resource.Warn, Summary: "~/.bashrc is no longer managed (left in place)"}})
	var progress bytes.Buffer

	result := Apply(t.Context(), e.plan(), ApplyInput{Store: e.store, Progress: &progress})
	want := []string{"apply tool:golang", "remove file:~/.bashrc", "remove tool:broken", "remove tool:kubectl"}
	if got := e.steps.get(); !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if got, want := e.ids(), []string{"repo:hashicorp", "tool:broken", "tool:golang"}; !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	wantOut := "Applied 1 change and 2 removals.\nCouldn't remove tool broken: permission denied\n"
	if b.String() != wantOut {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), wantOut)
	}
	wantProgress := "golang: + toolchain\n" +
		"prune: ! ~/.bashrc is no longer managed (left in place)\n" +
		"prune: - tool broken\n" +
		"prune: - tool kubectl\n"
	if progress.String() != wantProgress {
		t.Errorf("progress =\n%s\nwant\n%s", progress.String(), wantProgress)
	}
}

// TestApplyInterrupted presses Ctrl-C during the second step: it must
// finish, with a context that isn't cancelled, and nothing after it may
// run.
func TestApplyInterrupted(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	second := res("go-install:task", resource.Create, "task")
	second.onApply = func(stepCtx context.Context, _ *resource.Runtime) (state.Record, error) {
		cancel()
		if err := stepCtx.Err(); err != nil {
			t.Errorf("the step's context was cancelled: %v", err)
		}
		return state.Record{Details: map[string]string{"version": "3.41.0"}}, nil
	}
	e.add("golang", nil, res("tool:golang", resource.Create, "toolchain"), second, res("go-install:gopls", resource.Create, "gopls"))
	e.add("files", nil, res("file:~/.gitconfig", resource.Create, "~/.gitconfig"))
	e.record("tool:kubectl", "tool", "kubectl")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove}})

	result := e.apply(ctx)
	if got, want := e.steps.get(), []string{"apply tool:golang", "apply go-install:task"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if !result.Interrupted || result.OK() || result.ExitCode() == 0 {
		t.Errorf("Interrupted, OK(), ExitCode() = %t, %t, %d; want an interrupted result", result.Interrupted, result.OK(), result.ExitCode())
	}
	s, err := state.Load(e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.IDs(), []string{"go-install:task", "tool:golang", "tool:kubectl"}; !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
	if s.LastSyncResult != state.ResultInterrupted {
		t.Errorf("LastSyncResult = %q, want %q", s.LastSyncResult, state.ResultInterrupted)
	}
	var b bytes.Buffer
	result.Render(&b)
	if want := "Applied 2 changes.\nInterrupted: run maisys sync again to finish.\n"; b.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), want)
	}
}

// TestApplyAborted presses Ctrl-C twice during a step: the second time
// stops the step too.
func TestApplyAborted(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	abort, abortNow := context.WithCancel(t.Context())
	defer abortNow()
	r := res("tool:golang", resource.Create, "toolchain")
	r.onApply = func(stepCtx context.Context, _ *resource.Runtime) (state.Record, error) {
		cancel()
		abortNow()
		<-stepCtx.Done()
		return state.Record{}, stepCtx.Err()
	}
	e.add("golang", nil, r, res("go-install:task", resource.Create, "task"))

	result := Apply(ctx, e.plan(), ApplyInput{Store: e.store, Abort: abort})
	if !result.Interrupted || len(result.Modules) != 1 || !errors.Is(result.Modules[0].Err, context.Canceled) {
		t.Errorf("result = %+v, want an interrupted one with golang failed", result)
	}
	if got, want := e.steps.get(), []string{"apply tool:golang"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if e.store.LastSyncResult != state.ResultInterrupted {
		t.Errorf("LastSyncResult = %q, want %q", e.store.LastSyncResult, state.ResultInterrupted)
	}
}

func TestApplyNothing(t *testing.T) {
	e := newTestEnv(t)
	e.runner.SudoStatus = system.SudoNeedsPassword
	e.add("golang", nil, res("tool:golang", resource.NoChange, ""))
	e.record("tool:gone", "tool", "kubectl")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.NoChange}})

	result := e.apply(t.Context())
	if !result.OK() || len(e.steps.get()) > 0 || e.runner.SudoStarts() > 0 {
		t.Errorf("OK() = %t, steps = %q, sudo started %d times; want nothing done", result.OK(), e.steps.get(), e.runner.SudoStarts())
	}
	if ids := e.ids(); len(ids) > 0 {
		t.Errorf("records = %q, want the record of what's gone deleted", ids)
	}
	var b bytes.Buffer
	result.Render(&b)
	if want := "Nothing changed.\n"; b.String() != want {
		t.Errorf("Render() = %q, want %q", b.String(), want)
	}
	if e.store.LastSyncResult != state.ResultOK || !e.store.LastSync.Equal(testNow) {
		t.Errorf("last sync = %s %q, want an ok sync at %s", e.store.LastSync, e.store.LastSyncResult, testNow)
	}
}

func TestApplyUnknown(t *testing.T) {
	e := newTestEnv(t)
	e.runner.SudoStatus = system.SudoNeedsPassword
	e.runner.On("dpkg-query", func(context.Context, system.Cmd) (system.Result, error) { return system.Result{}, nil })
	r := &fakeResource{id: "package:jq", onCheck: func(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
		_, err := rt.Runner.Run(ctx, system.Cmd{Name: "dpkg-query", Root: true})
		return resource.Change{Summary: "install jq"}, err
	}}
	r.onApply = func(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
		_, err := rt.Runner.Run(ctx, system.Cmd{Name: "dpkg-query", Root: true})
		return state.Record{}, err
	}
	e.add("packages", nil, r)

	var progress bytes.Buffer
	result := Apply(t.Context(), e.plan(), ApplyInput{Store: e.store, Progress: &progress})
	if !result.OK() || e.runner.SudoStarts() != 1 {
		t.Errorf("OK() = %t, sudo started %d times; want the change applied in a sudo session", result.OK(), e.runner.SudoStarts())
	}
	if want := "packages: ? checking install jq\n"; progress.String() != want {
		t.Errorf("progress = %q, want %q", progress.String(), want)
	}
}

func TestApplyStateCantBeSaved(t *testing.T) {
	e := newTestEnv(t)
	first := res("tool:golang", resource.Create, "toolchain")
	first.onApply = func(context.Context, *resource.Runtime) (state.Record, error) {
		// A record without a kind can't be saved.
		e.store.Put(state.Record{ID: "broken"})
		return state.Record{}, nil
	}
	e.add("golang", nil, first, res("go-install:task", resource.Create, "task"))

	result := e.apply(t.Context())
	if result.Err == nil || result.OK() {
		t.Fatalf("Apply() = %+v, want an error", result)
	}
	if got, want := e.steps.get(), []string{"apply tool:golang"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	if !strings.HasPrefix(b.String(), "Nothing changed.\nStopped: saving the state file: ") {
		t.Errorf("Render() = %q, want it to say the sync stopped", b.String())
	}
}

// TestApplyRefreshesRecords applies a plan whose only effect is to bring
// records up to date: the plan has nothing to change and applying it shows
// nothing, but the records are saved, and only once it's applied.
func TestApplyRefreshesRecords(t *testing.T) {
	e := newTestEnv(t)
	before := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, rec := range []state.Record{
		{ID: "file:~/.gitconfig", Module: "files", Details: map[string]string{"sha256": "old"}},
		{ID: "file:~/.vimrc", Module: "dotfiles", Details: map[string]string{"sha256": "vim"}},
		{ID: "file:~/.zshrc", Module: "files", Details: map[string]string{"sha256": "zsh"}},
	} {
		rec.Kind, rec.Updated = "fake", before
		e.store.Put(rec)
	}
	if err := e.store.Save(); err != nil {
		t.Fatal(err)
	}
	edited := res("file:~/.gitconfig", resource.NoChange, "")
	edited.targets = []string{"~/.gitconfig"}
	edited.change.Record = &state.Record{Details: map[string]string{"sha256": "new"}}
	taken := res("file:~/.tmux.conf", resource.NoChange, "")
	taken.change.Record = &state.Record{Paths: []string{"~/.tmux.conf"}, Details: map[string]string{"sha256": "tmux"}, TookOver: true}
	e.add("files", nil, edited, res("file:~/.vimrc", resource.NoChange, ""), res("file:~/.zshrc", resource.NoChange, ""), taken)

	p := e.plan()
	var plan bytes.Buffer
	p.Render(&plan, false)
	if want := "Plan for phil-workp14s (ubuntu 26.04, amd64)\n\nNothing to change.\n"; plan.String() != want || p.HasChanges() {
		t.Errorf("Render() =\n%s\nHasChanges() = %t; want\n%s", plan.String(), p.HasChanges(), want)
	}
	saved, err := state.Load(e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := saved.Get("file:~/.gitconfig"); rec.Details["sha256"] != "old" {
		t.Errorf("the plan saved the record %+v, want it saved only when the plan is applied", rec)
	}

	var progress bytes.Buffer
	result := Apply(t.Context(), p, ApplyInput{Store: e.store, Progress: &progress})
	var b bytes.Buffer
	result.Render(&b)
	if b.String() != "Nothing changed.\n" || progress.Len() > 0 || len(e.steps.get()) > 0 {
		t.Errorf("Render() = %q, progress = %q, steps = %q; want nothing done or shown", b.String(), progress.String(), e.steps.get())
	}

	saved, err = state.Load(e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]state.Record{
		"file:~/.gitconfig": {Paths: []string{"~/.gitconfig"}, Details: map[string]string{"sha256": "new"}, Updated: testNow},
		"file:~/.vimrc":     {Details: map[string]string{"sha256": "vim"}, Updated: before},
		"file:~/.zshrc":     {Details: map[string]string{"sha256": "zsh"}, Updated: before},
		"file:~/.tmux.conf": {Paths: []string{"~/.tmux.conf"}, Details: map[string]string{"sha256": "tmux"}, TookOver: true, Updated: testNow},
	}
	for id, w := range want {
		w.ID, w.Kind, w.Module = id, "fake", "files"
		if got, ok := saved.Get(id); !ok || !reflect.DeepEqual(got, w) {
			t.Errorf("saved record of %s = %+v, want %+v", id, got, w)
		}
	}
}

// TestApplyRefreshesOnlyAppliedModules checks that the records of modules
// that failed or were skipped aren't brought up to date.
func TestApplyRefreshesOnlyAppliedModules(t *testing.T) {
	e := newTestEnv(t)
	for _, id := range []string{"file:~/.bashrc", "file:~/.inputrc", "file:~/.vimrc"} {
		e.store.Put(state.Record{ID: id, Kind: "fake", Module: "old"})
	}
	refreshed := func(id string) *fakeResource {
		r := res(id, resource.NoChange, "")
		r.change.Record = &state.Record{Details: map[string]string{"sha256": "new"}}
		return r
	}
	failing := res("file:~/.profile", resource.Create, "~/.profile")
	failing.checkErr = errors.New("permission denied")
	e.add("broken", nil, refreshed("file:~/.bashrc"), failing)
	e.add("skipped", []string{"broken"}, refreshed("file:~/.inputrc"))
	e.add("files", nil, refreshed("file:~/.vimrc"))

	e.apply(t.Context())
	saved, err := state.Load(e.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	for id, module := range map[string]string{"file:~/.bashrc": "old", "file:~/.inputrc": "old", "file:~/.vimrc": "files"} {
		if rec, _ := saved.Get(id); rec.Module != module || (module == "files") != (rec.Details["sha256"] == "new") {
			t.Errorf("saved record of %s = %+v, want it refreshed only if its module was applied", id, rec)
		}
	}
}
