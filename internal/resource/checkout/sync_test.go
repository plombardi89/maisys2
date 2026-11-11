package checkout

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
)

// checkoutsModule is a module whose resources are checkouts.
type checkoutsModule struct{ checkouts []*Checkout }

func (m *checkoutsModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: "shell", Summary: "Checkouts.", OS: config.AllOS()}
}

func (m *checkoutsModule) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (m *checkoutsModule) DependsOn() []string { return nil }

func (m *checkoutsModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	rs := make([]resource.Resource, len(m.checkouts))
	for i, c := range m.checkouts {
		rs[i] = c
	}
	return rs, nil
}

// sync plans and applies the checkouts, as maisys sync does, and returns
// the plan as it's shown.
func (sb *sandbox) sync(store *state.Store, checkouts ...*Checkout) string {
	sb.t.Helper()
	machine := &config.Machine{Name: "box", OS: config.Ubuntu, Modules: map[string]config.ModuleConfig{"shell": &config.Toggle{Enabled: true}}}
	p, err := engine.NewPlan(sb.t.Context(), engine.PlanInput{
		Machine:  machine,
		Facts:    &facts.System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", Arch: config.AMD64, RawArch: "amd64"},
		Paths:    settings.Paths{Home: sb.home},
		Runner:   sb.rt.Runner,
		State:    store.State,
		Modules:  []config.Module{&checkoutsModule{checkouts: checkouts}},
		Removers: resource.NewRemovers(Remover{Env: sb.env}),
		Now:      func() time.Time { return testNow },
	})
	if err != nil {
		sb.t.Fatal(err)
	}
	var b bytes.Buffer
	p.Render(&b, false)
	if res := engine.Apply(sb.t.Context(), p, engine.ApplyInput{Store: store}); !res.OK() {
		var out bytes.Buffer
		res.Render(&out)
		sb.t.Fatalf("applying failed:\n%s", out.String())
	}
	return b.String()
}

// TestSyncReplacesParent syncs oh-my-zsh, which is a clone of another
// repo, and a plugin inside it that's checked out as it should be:
// replacing oh-my-zsh moves the plugin aside with it, so the same sync
// clones the plugin again, and the next has nothing to change.
func TestSyncReplacesParent(t *testing.T) {
	sb := newSandbox(t)
	omz := sb.upstream("ohmyzsh")
	omz.commit("oh-my-zsh.sh", "omz")
	old := sb.upstream("old")
	old.commit("oh-my-zsh.sh", "old")
	plugin := sb.upstream("plugin")
	plugin.commit("plugin.zsh", "one")

	dir := filepath.Join(sb.home, ".oh-my-zsh")
	sb.git("", "clone", "--quiet", old.url, dir)
	child := sb.checkout("plugin", plugin.url, "main")
	sb.git("", "clone", "--quiet", plugin.url, child.Dir)
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkouts := func() []*Checkout {
		return []*Checkout{
			{Name: "oh-my-zsh", Repo: omz.url, Ref: "main", Dir: dir, Env: sb.env},
			sb.checkout("plugin", plugin.url, "main"),
		}
	}

	got := sb.sync(store, checkouts()...)
	if want := "  ! oh-my-zsh: ~/.oh-my-zsh isn't a checkout of " + omz.url + " (will back up and replace)\n\n1 to change.\n"; !strings.HasSuffix(got, want) {
		t.Errorf("plan:\n%s\nwant it to end with:\n%s", got, want)
	}
	assertContent(t, filepath.Join(dir, "oh-my-zsh.sh"), "omz")
	assertContent(t, filepath.Join(child.Dir, "plugin.zsh"), "one")
	assertContent(t, filepath.Join(dir+".maisys-bak.20261106-093000", "custom", "plugins", "plugin", "plugin.zsh"), "one")
	if rec, ok := store.Get(child.ID()); !ok || rec.Details["commit"] != sb.head(child.Dir) {
		t.Errorf("the plugin's record = %+v, want the commit cloned again", rec)
	}

	if got := sb.sync(store, checkouts()...); !strings.HasSuffix(got, "\nNothing to change.\n") {
		t.Errorf("plan after the sync:\n%s\nwant nothing to change", got)
	}
}

// TestSyncLeavesInPlace takes out of the config a plugin you've since
// replaced with your own clone of another repo: the plan notes it's left
// in place, once, with nothing to change, and its record is dropped.
func TestSyncLeavesInPlace(t *testing.T) {
	sb := newSandbox(t)
	plugin := sb.upstream("plugin")
	plugin.commit("plugin.zsh", "one")
	mine := sb.upstream("mine")
	mine.commit("plugin.zsh", "mine")
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := sb.checkout("plugin", plugin.url, "main")
	sb.sync(store, c)
	if err := os.RemoveAll(c.Dir); err != nil {
		t.Fatal(err)
	}
	sb.git("", "clone", "--quiet", mine.url, c.Dir)

	got := sb.sync(store)
	want := "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"prune\n" +
		"  ! ~/.oh-my-zsh/custom/plugins/plugin isn't a checkout of " + plugin.url + " any more (left in place)\n\n" +
		"Nothing to change.\n"
	if got != want {
		t.Errorf("plan after taking the plugin out:\n%s\nwant:\n%s", got, want)
	}
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "mine")
	if ids := store.IDs(); len(ids) > 0 {
		t.Errorf("records = %q, want none", ids)
	}
	if got := sb.sync(store); got != "Plan for box (ubuntu 26.04, amd64)\n\nNothing to change.\n" {
		t.Errorf("the next plan:\n%s\nwant nothing to change", got)
	}
}
