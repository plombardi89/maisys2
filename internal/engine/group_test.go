package engine

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
)

// fakeGrouped is a resource in the group "packages". ApplyGroup records the
// IDs of the members it's called with as one step, and fails the members
// that have an applyErr.
type fakeGrouped struct {
	*fakeResource
}

// grouped returns a resource in the group "packages" whose check finds a
// change to create, described by summary.
func grouped(id, summary string) *fakeGrouped {
	return &fakeGrouped{fakeResource: res(id, resource.Create, summary)}
}

func (r *fakeGrouped) Group() string { return "packages" }

func (r *fakeGrouped) base() *fakeResource { return r.fakeResource }

func (r *fakeGrouped) ApplyGroup(_ context.Context, _ *resource.Runtime, members []resource.Member) []resource.Outcome {
	ids := make([]string, len(members))
	outcomes := make([]resource.Outcome, len(members))
	for i, m := range members {
		member, ok := m.Resource.(interface{ base() *fakeResource })
		if !ok {
			panic("a member isn't a fakeGrouped")
		}
		ids[i] = member.base().id
		outcomes[i] = resource.Outcome{Record: state.Record{Details: map[string]string{"module": m.Module}}, Err: member.base().applyErr}
	}
	r.steps.add("group " + strings.Join(ids, " "))
	return outcomes
}

// addAll adds an enabled module with resources rs, which record their
// steps, as add does for fake resources.
func (e *testEnv) addAll(name string, deps []string, rs ...resource.Resource) {
	m := e.add(name, deps)
	for _, r := range rs {
		switch r := r.(type) {
		case interface{ base() *fakeResource }:
			r.base().steps = e.steps
		case *fakeResource:
			r.steps = e.steps
		}
		m.resources = append(m.resources, r)
	}
}

func TestApplyGroup(t *testing.T) {
	e := newTestEnv(t)
	brokenCheck := res("fake:broken", resource.Create, "broken")
	brokenCheck.checkErr = errors.New("lookup failed")
	unchanged := grouped("pkg:tmux", "")
	unchanged.change = resource.Change{}
	e.addAll("files", nil, res("file:x", resource.Create, "~/x"))
	e.addAll("packages", nil, grouped("pkg:jq", "install jq"), unchanged, grouped("pkg:htop", "install htop"))
	e.addAll("broken", nil, grouped("pkg:broken", "install broken"), brokenCheck)
	e.addAll("shell", nil, grouped("pkg:zsh", "install zsh"), res("file:rc", resource.Create, "~/.zshrc"), grouped("pkg:fish", "install fish"))
	e.addAll("hashicorp", nil, res("repo:hashicorp", resource.Create, "add repo hashicorp"), grouped("pkg:packer", "install packer"))
	e.addAll("late", []string{"files"}, grouped("pkg:late", "install late"))
	e.addAll("after", []string{"hashicorp"}, grouped("pkg:after", "install after"))
	var progress bytes.Buffer

	result := Apply(t.Context(), e.plan(), ApplyInput{Store: e.store, Progress: &progress})
	want := []string{
		"apply file:x",
		// shell's change after its rc file has to wait, as do hashicorp's
		// after its repo and after's after hashicorp. broken failed.
		"group pkg:jq pkg:htop pkg:zsh pkg:late",
		"apply file:rc",
		"group pkg:fish",
		"apply repo:hashicorp",
		"group pkg:packer",
		"group pkg:after",
	}
	if got := e.steps.get(); !slices.Equal(got, want) {
		t.Errorf("steps =\n%q\nwant\n%q", got, want)
	}
	wantProgress := "files: + ~/x\n" +
		"packages: + install jq\npackages: + install htop\nshell: + install zsh\nlate: + install late\n" +
		"shell: + ~/.zshrc\n" +
		"shell: + install fish\n" +
		"hashicorp: + add repo hashicorp\n" +
		"hashicorp: + install packer\n" +
		"after: + install after\n"
	if progress.String() != wantProgress {
		t.Errorf("progress =\n%s\nwant\n%s", progress.String(), wantProgress)
	}

	applied := map[string]int{}
	for _, m := range result.Modules {
		applied[m.Name] = m.Applied
	}
	wantApplied := map[string]int{"files": 1, "packages": 2, "broken": 0, "shell": 3, "hashicorp": 2, "late": 1, "after": 1}
	for name, n := range wantApplied {
		if applied[name] != n {
			t.Errorf("module %s: applied %d changes, want %d", name, applied[name], n)
		}
	}
	rec, ok := e.store.Get("pkg:zsh")
	if !ok || rec.Module != "shell" || rec.Kind != "fake" || rec.Details["module"] != "shell" || !rec.Updated.Equal(testNow) {
		t.Errorf("record of pkg:zsh = %+v, %t; want it filled in for module shell", rec, ok)
	}
	wantIDs := []string{"file:rc", "file:x", "pkg:after", "pkg:fish", "pkg:htop", "pkg:jq", "pkg:late", "pkg:packer", "pkg:zsh", "repo:hashicorp"}
	if got := e.ids(); !slices.Equal(got, wantIDs) {
		t.Errorf("records = %q, want %q", got, wantIDs)
	}
}

func TestApplyGroupFailures(t *testing.T) {
	e := newTestEnv(t)
	bad := grouped("pkg:bad", "install bad")
	bad.applyErr = errors.New("E: Unable to locate package bad")
	podman := grouped("pkg:podman", "install podman")
	podman.applyErr = errors.New("dependency problems")
	e.addAll("packages", nil, grouped("pkg:jq", "install jq"), bad, grouped("pkg:htop", "install htop"), res("file:after-bad", resource.Create, "after bad"))
	e.addAll("podman", nil, podman, res("unit:podman", resource.Create, "podman.socket"))
	e.addAll("shell", nil, grouped("pkg:zsh", "install zsh"), res("file:rc", resource.Create, "~/.zshrc"))
	e.addAll("compose", []string{"podman"}, grouped("pkg:compose", "install podman-compose"))

	result := e.apply(t.Context())
	want := []string{"group pkg:jq pkg:bad pkg:htop pkg:podman pkg:zsh", "apply file:rc"}
	if got := e.steps.get(); !slices.Equal(got, want) {
		t.Errorf("steps =\n%q\nwant\n%q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	wantOut := "Applied 4 changes.\n" +
		"packages failed: install bad: E: Unable to locate package bad\n" +
		"podman failed: install podman: dependency problems\n" +
		"compose skipped because podman failed.\n"
	if b.String() != wantOut {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), wantOut)
	}
	if got, want := e.ids(), []string{"file:rc", "pkg:htop", "pkg:jq", "pkg:zsh"}; !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
}

func TestApplyGroupWithoutSudo(t *testing.T) {
	e := newTestEnv(t)
	e.runner.StartErr = errors.New("sudo couldn't check your password: sudo: 3 incorrect password attempts")
	e.addAll("packages", nil, grouped("pkg:jq", "install jq").sudo(), grouped("pkg:htop", "install htop"))
	e.addAll("shell", nil, grouped("pkg:zsh", "install zsh"))

	result := e.apply(t.Context())
	if got, want := e.steps.get(), []string{"group pkg:zsh"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	var b bytes.Buffer
	result.Render(&b)
	wantOut := "Applied 1 change.\npackages failed: install jq: sudo couldn't check your password: sudo: 3 incorrect password attempts\n"
	if b.String() != wantOut {
		t.Errorf("Render() =\n%s\nwant\n%s", b.String(), wantOut)
	}
}

// sudo makes r need root.
func (r *fakeGrouped) sudo() *fakeGrouped {
	r.privileged = true
	return r
}

// TestApplyGroupInterrupted presses Ctrl-C while a group is applied: the
// step finishes, and its records are saved, but nothing after it runs.
func TestApplyGroupInterrupted(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.addAll("packages", nil, &interruptingGroup{fakeGrouped: grouped("pkg:jq", "install jq"), cancel: cancel}, res("file:x", resource.Create, "x"))
	e.addAll("shell", nil, grouped("pkg:zsh", "install zsh"))

	result := e.apply(ctx)
	if got, want := e.steps.get(), []string{"group pkg:jq pkg:zsh"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if !result.Interrupted {
		t.Errorf("result = %+v, want it interrupted", result)
	}
	if got, want := e.ids(), []string{"pkg:jq", "pkg:zsh"}; !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
}

// interruptingGroup presses Ctrl-C while its group is applied.
type interruptingGroup struct {
	*fakeGrouped
	cancel context.CancelFunc
}

func (r *interruptingGroup) ApplyGroup(ctx context.Context, rt *resource.Runtime, members []resource.Member) []resource.Outcome {
	r.cancel()
	return r.fakeGrouped.ApplyGroup(ctx, rt, members)
}
