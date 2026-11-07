package checkout

import (
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// testNow is the time tests run at.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// sandbox is a home folder and git setup of its own, so tests never read or
// change the user's git config. It keeps the records Apply returns, as the
// state file would.
type sandbox struct {
	t       *testing.T
	home    string
	env     []string
	rt      *resource.Runtime
	records map[string]state.Record
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("git checkouts are only made on Linux")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("these tests need git: %v", err)
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sb := &sandbox{
		t:    t,
		home: home,
		env: []string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + global,
			"GIT_AUTHOR_NAME=maisys test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=maisys test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		},
		records: map[string]state.Record{},
	}
	sb.rt = &resource.Runtime{
		Runner:   system.NewRunner(system.Options{}),
		Paths:    settings.Paths{Home: home},
		Previous: func(id string) (state.Record, bool) { rec, ok := sb.records[id]; return rec, ok },
		Now:      func() time.Time { return testNow },
	}
	return sb
}

// git runs git in dir and returns its trimmed output.
func (sb *sandbox) git(dir string, args ...string) string {
	sb.t.Helper()
	return runGit(sb.t, sb.env, dir, args...)
}

// runGit runs git in dir with env and returns its trimmed output.
func runGit(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// upstream is a repo that checkouts are made from.
type upstream struct {
	sb  *sandbox
	dir string
	url string
}

// upstream creates a repo with a main branch.
func (sb *sandbox) upstream(name string) *upstream {
	sb.t.Helper()
	dir := filepath.Join(sb.t.TempDir(), name)
	sb.git("", "init", "--quiet", "--initial-branch=main", dir)
	return &upstream{sb: sb, dir: dir, url: "file://" + dir}
}

// commit writes content to file on the current branch and commits it. It
// returns the commit's hash.
func (u *upstream) commit(file, content string) string {
	u.sb.t.Helper()
	writeFile(u.sb.t, filepath.Join(u.dir, file), content)
	u.sb.git(u.dir, "add", "--all")
	u.sb.git(u.dir, "commit", "--quiet", "--message", "set "+file+" to "+content)
	return u.sb.git(u.dir, "rev-parse", "HEAD")
}

// checkout returns a checkout of the repo at ref, in the sandbox's home.
func (sb *sandbox) checkout(name, url, ref string) *Checkout {
	return &Checkout{
		Name: name,
		Repo: url,
		Ref:  ref,
		Dir:  filepath.Join(sb.home, ".oh-my-zsh", "custom", "plugins", name),
		Env:  sb.env,
	}
}

// check checks c, failing the test if the check fails.
func (sb *sandbox) check(c *Checkout) resource.Change {
	sb.t.Helper()
	ch, err := c.Check(sb.t.Context(), sb.rt)
	if err != nil {
		sb.t.Fatalf("Check() error = %v", err)
	}
	return ch
}

// apply applies c, as the engine does after Check, and keeps its record.
func (sb *sandbox) apply(c *Checkout) state.Record {
	sb.t.Helper()
	rec, err := c.Apply(sb.t.Context(), sb.rt)
	if err != nil {
		sb.t.Fatalf("Apply() error = %v", err)
	}
	rec.ID, rec.Kind = c.ID(), c.Kind()
	sb.records[rec.ID] = rec
	return rec
}

// head returns the commit checked out in dir.
func (sb *sandbox) head(dir string) string {
	sb.t.Helper()
	return sb.git(dir, "rev-parse", "HEAD")
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func assertChange(t *testing.T, got resource.Change, want resource.Change) {
	t.Helper()
	if got.Action != want.Action || got.Summary != want.Summary || !slices.Equal(got.Notes, want.Notes) {
		t.Errorf("Check() = %s %q %q, want %s %q %q", got.Action, got.Summary, got.Notes, want.Action, want.Summary, want.Notes)
	}
}

// assertNoStages checks that no clone was left behind next to dir.
func assertNoStages(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".maisys-") && !strings.Contains(e.Name(), ".maisys-bak.") {
			t.Errorf("%s was left behind", filepath.Join(filepath.Dir(dir), e.Name()))
		}
	}
}

func TestBranch(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	first := up.commit("plugin.zsh", "one")

	c := sb.checkout("plugin", up.url, "main")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Create, Summary: "plugin (main)"})
	rec := sb.apply(c)
	want := map[string]string{"name": "plugin", "repo": up.url, "ref": "main", "ref_type": "branch", "commit": first}
	if !maps.Equal(rec.Details, want) || !slices.Equal(rec.Paths, []string{c.Dir}) {
		t.Errorf("Apply() = %+v, want details %v and paths [%s]", rec, want, c.Dir)
	}
	if got := sb.head(c.Dir); got != first {
		t.Errorf("checked out %s, want %s", got, first)
	}
	if got := sb.git(c.Dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the checkout isn't shallow")
	}
	if got := sb.git(c.Dir, "remote", "get-url", "origin"); got != up.url {
		t.Errorf("origin = %q, want %q", got, up.url)
	}
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "one")
	assertNoStages(t, c.Dir)
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "main")), resource.Change{})

	// The branch moves: the next sync follows it.
	second := up.commit("plugin.zsh", "two")
	c = sb.checkout("plugin", up.url, "main")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Update, Summary: "plugin " + first[:7] + " → " + second[:7] + " (main)"})
	if rec := sb.apply(c); rec.Details["commit"] != second {
		t.Errorf("Apply() recorded commit %s, want %s", rec.Details["commit"], second)
	}
	if got := sb.head(c.Dir); got != second {
		t.Errorf("checked out %s, want %s", got, second)
	}
	if got := sb.git(c.Dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the checkout isn't shallow after an update")
	}
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "two")
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "main")), resource.Change{})
}

func TestTag(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	first := up.commit("plugin.zsh", "one")
	sb.git(up.dir, "tag", "--annotate", "--message", "v1", "v1")
	second := up.commit("plugin.zsh", "two")
	sb.git(up.dir, "tag", "v2")
	up.commit("plugin.zsh", "three")

	c := sb.checkout("plugin", up.url, "v1")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Create, Summary: "plugin (v1)"})
	if rec := sb.apply(c); rec.Details["ref_type"] != "tag" || rec.Details["commit"] != first {
		t.Errorf("Apply() recorded %v, want the commit of tag v1 (%s)", rec.Details, first)
	}
	if got := sb.head(c.Dir); got != first {
		t.Errorf("checked out %s, want the commit of the annotated tag, %s", got, first)
	}
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "one")

	// A tag that moves stays where it was checked out.
	sb.git(up.dir, "tag", "--force", "--annotate", "--message", "moved", "v1", second)
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "v1")), resource.Change{})

	// Without a record, the tag is looked up.
	delete(sb.records, c.ID())
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "v1")),
		resource.Change{Action: resource.Update, Summary: "plugin " + first[:7] + " → " + second[:7] + " (v1)"})

	// Another tag in the config.
	c = sb.checkout("plugin", up.url, "v2")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Update, Summary: "plugin " + first[:7] + " → " + second[:7] + " (v2)"})
	sb.apply(c)
	if got := sb.head(c.Dir); got != second {
		t.Errorf("checked out %s, want the commit of tag v2, %s", got, second)
	}
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "v2")), resource.Change{})
}

func TestCommit(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	first := up.commit("plugin.zsh", "one")
	second := up.commit("plugin.zsh", "two")
	up.commit("plugin.zsh", "three")

	c := sb.checkout("plugin", up.url, second)
	assertChange(t, sb.check(c), resource.Change{Action: resource.Create, Summary: "plugin (" + second[:7] + ")"})
	if rec := sb.apply(c); rec.Details["ref_type"] != "commit" || rec.Details["commit"] != second {
		t.Errorf("Apply() recorded %v, want commit %s", rec.Details, second)
	}
	if got := sb.head(c.Dir); got != second {
		t.Errorf("checked out %s, want %s", got, second)
	}

	// A full hash is checked without the repo.
	moved := up.dir + ".moved"
	if err := os.Rename(up.dir, moved); err != nil {
		t.Fatal(err)
	}
	assertChange(t, sb.check(sb.checkout("plugin", up.url, second)), resource.Change{})
	if err := os.Rename(moved, up.dir); err != nil {
		t.Fatal(err)
	}

	// An abbreviated hash is looked up once, then stays where it is.
	abbrev := first[:9]
	c = sb.checkout("plugin", up.url, abbrev)
	assertChange(t, sb.check(c), resource.Change{Action: resource.Update, Summary: "plugin " + second[:7] + " → " + abbrev})
	if rec := sb.apply(c); rec.Details["commit"] != first {
		t.Errorf("Apply() recorded commit %s, want %s", rec.Details["commit"], first)
	}
	if got := sb.head(c.Dir); got != first {
		t.Errorf("checked out %s, want %s", got, first)
	}
	if err := os.Rename(up.dir, moved); err != nil {
		t.Fatal(err)
	}
	assertChange(t, sb.check(sb.checkout("plugin", up.url, abbrev)), resource.Change{})
	if err := os.Rename(moved, up.dir); err != nil {
		t.Fatal(err)
	}

	// A new checkout at an abbreviated hash.
	c = sb.checkout("other", up.url, abbrev)
	assertChange(t, sb.check(c), resource.Change{Action: resource.Create, Summary: "other (" + abbrev + ")"})
	sb.apply(c)
	if got := sb.head(c.Dir); got != first {
		t.Errorf("checked out %s, want %s", got, first)
	}
}

func TestCommitNotAdvertised(t *testing.T) {
	// With git's protocol version 0, a repo only sends the commits its
	// branches and tags point to, unless it's set up otherwise; a commit
	// in their history is fetched with everything else.
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")
	second := up.commit("plugin.zsh", "two")
	up.commit("plugin.zsh", "three")

	c := sb.checkout("plugin", up.url, second)
	c.Env = append(slices.Clone(sb.env), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.version", "GIT_CONFIG_VALUE_0=0")
	sb.check(c)
	sb.apply(c)
	if got := sb.head(c.Dir); got != second {
		t.Errorf("checked out %s, want %s", got, second)
	}
}

func TestCheckErrors(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")

	tests := []struct {
		name string
		c    *Checkout
		want string
	}{
		{"missing ref", sb.checkout("plugin", up.url, "nope"), "nope isn't a branch or tag of " + up.url},
		{"missing repo", sb.checkout("plugin", up.url+"-missing", "main"), "couldn't look up main in " + up.url + "-missing: git ls-remote failed"},
		{"no name", &Checkout{Repo: up.url, Ref: "main", Dir: sb.home}, "a git checkout has no name"},
		{"bad ref", sb.checkout("plugin", up.url, "a b"), `plugin: "a b" isn't a branch, tag or commit`},
		{"option ref", sb.checkout("plugin", up.url, "--upload-pack=x"), `isn't a branch, tag or commit`},
		{"option repo", sb.checkout("plugin", "--upload-pack=x", "main"), `plugin: "--upload-pack=x" isn't a repo URL`},
		{"relative dir", &Checkout{Name: "plugin", Repo: up.url, Ref: "main", Dir: "plugins/plugin"}, "isn't an absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.c.Check(t.Context(), sb.rt)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Check() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestFailedCloneLeavesNothing(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")

	missing := strings.Repeat("0123456789", 4)
	c := sb.checkout("plugin", up.url, missing)
	assertChange(t, sb.check(c), resource.Change{Action: resource.Create, Summary: "plugin (0123456)"})
	_, err := c.Apply(t.Context(), sb.rt)
	if err == nil || !strings.Contains(err.Error(), up.url+" has no commit "+missing) {
		t.Errorf("Apply() error = %v, want one saying the commit is missing", err)
	}
	if _, err := os.Lstat(c.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Apply() left %s behind (%v)", c.Dir, err)
	}
	assertNoStages(t, c.Dir)
}

func TestStaleStagesRemoved(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")

	c := sb.checkout("plugin", up.url, "main")
	stale := filepath.Join(filepath.Dir(c.Dir), ".plugin.maisys-1234", "plugin")
	writeFile(t, filepath.Join(stale, "plugin.zsh"), "half")
	sb.check(c)
	sb.apply(c)
	assertNoStages(t, c.Dir)
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "one")
}

func TestLocalChanges(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")
	c := sb.checkout("plugin", up.url, "main")
	sb.check(c)
	sb.apply(c)

	writeFile(t, filepath.Join(c.Dir, "plugin.zsh"), "edited")
	writeFile(t, filepath.Join(c.Dir, "mine.zsh"), "untracked")
	shown := "~/.oh-my-zsh/custom/plugins/plugin"
	c = sb.checkout("plugin", up.url, "main")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Warn, Summary: shown + " has local changes (will be discarded)"})
	sb.apply(c)
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "one")
	assertContent(t, filepath.Join(c.Dir, "mine.zsh"), "untracked")
	assertChange(t, sb.check(sb.checkout("plugin", up.url, "main")), resource.Change{})

	// Local changes and a new commit.
	head := sb.head(c.Dir)
	second := up.commit("plugin.zsh", "two")
	writeFile(t, filepath.Join(c.Dir, "plugin.zsh"), "edited")
	c = sb.checkout("plugin", up.url, "main")
	assertChange(t, sb.check(c), resource.Change{
		Action:  resource.Update,
		Summary: "plugin " + head[:7] + " → " + second[:7] + " (main)",
		Notes:   []string{shown + " has local changes (will be discarded)"},
	})
	sb.apply(c)
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "two")
	assertContent(t, filepath.Join(c.Dir, "mine.zsh"), "untracked")
}

func TestReplace(t *testing.T) {
	shown := "~/.oh-my-zsh/custom/plugins/plugin"
	// In the tests' summaries, UP is the repo's URL.
	tests := []struct {
		name    string
		setup   func(t *testing.T, sb *sandbox, up *upstream, dir string)
		want    resource.Change
		kept    string // a file the backup keeps
		backups int
	}{
		{
			name: "empty directory",
			setup: func(t *testing.T, _ *sandbox, _ *upstream, dir string) {
				t.Helper()
				t.Helper()
				mkdir(t, dir)
			},
			want: resource.Change{Action: resource.Create, Summary: "plugin (main)"},
		},
		{
			name: "not a checkout",
			setup: func(t *testing.T, _ *sandbox, _ *upstream, dir string) {
				t.Helper()
				writeFile(t, filepath.Join(dir, "mine.zsh"), "mine")
			},
			want:    resource.Change{Action: resource.Warn, Summary: "plugin: " + shown + " isn't a git checkout (will back up and replace)"},
			kept:    "mine.zsh",
			backups: 1,
		},
		{
			name: "a file",
			setup: func(t *testing.T, _ *sandbox, _ *upstream, dir string) {
				t.Helper()
				t.Helper()
				writeFile(t, dir, "mine")
			},
			want:    resource.Change{Action: resource.Warn, Summary: "plugin: " + shown + " isn't a directory (will back up and replace)"},
			backups: 1,
		},
		{
			name: "a symlink",
			setup: func(t *testing.T, _ *sandbox, _ *upstream, dir string) {
				t.Helper()
				mkdir(t, filepath.Dir(dir))
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			},
			want:    resource.Change{Action: resource.Warn, Summary: "plugin: " + shown + " is a symlink (will back up and replace)"},
			backups: 1,
		},
		{
			name: "a checkout of another repo",
			setup: func(_ *testing.T, sb *sandbox, _ *upstream, dir string) {
				other := sb.upstream("other")
				other.commit("other.zsh", "other")
				sb.git("", "clone", "--quiet", other.url, dir)
			},
			want:    resource.Change{Action: resource.Warn, Summary: "plugin: " + shown + " isn't a checkout of UP (will back up and replace)"},
			kept:    "other.zsh",
			backups: 1,
		},
		{
			name: "a broken checkout",
			setup: func(t *testing.T, _ *sandbox, _ *upstream, dir string) {
				t.Helper()
				writeFile(t, filepath.Join(dir, ".git"), "gitdir: /nowhere\n")
			},
			want:    resource.Change{Action: resource.Warn, Summary: "plugin: " + shown + " isn't a git checkout (will back up and replace)"},
			backups: 1,
		},
		{
			name: "the same repo, cloned by hand",
			setup: func(_ *testing.T, sb *sandbox, up *upstream, dir string) {
				sb.git("", "clone", "--quiet", up.url, dir)
				sb.git(dir, "remote", "set-url", "origin", up.url+".git/")
			},
			want: resource.Change{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := newSandbox(t)
			up := sb.upstream("plugin")
			up.commit("plugin.zsh", "one")
			c := sb.checkout("plugin", up.url, "main")
			tt.setup(t, sb, up, c.Dir)
			want := tt.want
			want.Summary = strings.ReplaceAll(want.Summary, "UP", up.url)
			assertChange(t, sb.check(c), want)
			if want.Action == resource.NoChange {
				return
			}
			sb.apply(c)
			assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "one")
			assertNoStages(t, c.Dir)
			backups, err := filepath.Glob(c.Dir + ".maisys-bak.*")
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != tt.backups {
				t.Fatalf("backups = %q, want %d", backups, tt.backups)
			}
			if tt.backups > 0 && backups[0] != c.Dir+".maisys-bak.20261106-093000" {
				t.Errorf("backup = %s, want it named after the time", backups[0])
			}
			if tt.kept != "" {
				if _, err := os.Stat(filepath.Join(backups[0], tt.kept)); err != nil {
					t.Errorf("the backup lost %s: %v", tt.kept, err)
				}
			}
			assertChange(t, sb.check(sb.checkout("plugin", up.url, "main")), resource.Change{})
		})
	}
}

func TestRepoChanged(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")
	fork := sb.upstream("fork")
	forked := fork.commit("plugin.zsh", "forked")

	c := sb.checkout("plugin", up.url, "main")
	sb.check(c)
	sb.apply(c)
	writeFile(t, filepath.Join(c.Dir, "cache.zwc"), "compiled")

	c = sb.checkout("plugin", fork.url, "main")
	assertChange(t, sb.check(c), resource.Change{Action: resource.Update, Summary: "plugin " + up.url + " → " + fork.url + " (main)"})
	if rec := sb.apply(c); rec.Details["repo"] != fork.url || rec.Details["commit"] != forked {
		t.Errorf("Apply() recorded %v, want the fork", rec.Details)
	}
	assertContent(t, filepath.Join(c.Dir, "plugin.zsh"), "forked")
	if _, err := os.Stat(filepath.Join(c.Dir, "cache.zwc")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old checkout's files are still there (%v)", err)
	}
	if got := sb.git(c.Dir, "remote", "get-url", "origin"); got != fork.url {
		t.Errorf("origin = %q, want %q", got, fork.url)
	}
	if backups, _ := filepath.Glob(c.Dir + ".maisys-bak.*"); backups != nil {
		t.Errorf("backups = %q, want none: maisys made the old checkout", backups)
	}
	assertNoStages(t, c.Dir)
	assertChange(t, sb.check(sb.checkout("plugin", fork.url, "main")), resource.Change{})
}

func TestRemover(t *testing.T) {
	sb := newSandbox(t)
	up := sb.upstream("plugin")
	up.commit("plugin.zsh", "one")
	c := sb.checkout("plugin", up.url, "main")
	sb.check(c)
	rec := sb.apply(c)
	r := Remover{Env: sb.env}
	shown := "~/.oh-my-zsh/custom/plugins/plugin"
	if r.Kind() != Kind {
		t.Errorf("Kind() = %q, want %q", r.Kind(), Kind)
	}

	checkRemove := func(want resource.Change) {
		t.Helper()
		got, err := r.CheckRemove(t.Context(), sb.rt, rec)
		if err != nil {
			t.Fatal(err)
		}
		assertChange(t, got, want)
	}

	checkRemove(resource.Change{Action: resource.Remove, Summary: "plugin (" + shown + ")"})
	writeFile(t, filepath.Join(c.Dir, "plugin.zsh"), "edited")
	checkRemove(resource.Change{
		Action:  resource.Remove,
		Summary: "plugin (" + shown + ")",
		Notes:   []string{shown + " has local changes (will be deleted too)"},
	})
	if err := r.Remove(t.Context(), sb.rt, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(c.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Remove() left %s (%v)", c.Dir, err)
	}
	checkRemove(resource.Change{})
	if err := r.Remove(t.Context(), sb.rt, rec); err != nil {
		t.Errorf("Remove() of a checkout that's gone: error = %v", err)
	}

	// Something that isn't a checkout any more is left in place.
	writeFile(t, filepath.Join(c.Dir, "mine.zsh"), "mine")
	checkRemove(resource.Change{Action: resource.Warn, Summary: shown + " isn't a git checkout any more (left in place)"})
	if err := r.Remove(t.Context(), sb.rt, rec); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(c.Dir, "mine.zsh"), "mine")
}

func TestSameRepo(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://github.com/ohmyzsh/ohmyzsh.git", "https://github.com/ohmyzsh/ohmyzsh", true},
		{"https://github.com/ohmyzsh/ohmyzsh/", "https://github.com/ohmyzsh/ohmyzsh.git", true},
		{"https://github.com/ohmyzsh/ohmyzsh", "https://github.com/ohmyzsh/ohmyzsh", true},
		{"https://github.com/ohmyzsh/ohmyzsh", "https://github.com/zsh-users/ohmyzsh", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := sameRepo(tt.a, tt.b); got != tt.want {
			t.Errorf("sameRepo(%q, %q) = %t, want %t", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTarget(t *testing.T) {
	head := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		target target
		at     bool
		label  string
	}{
		{target{kind: refBranch, commit: head}, true, "0123456"},
		{target{kind: refBranch, commit: strings.Repeat("f", 40)}, false, "fffffff"},
		{target{kind: refCommit, abbrev: "012345678"}, true, "012345678"},
		{target{kind: refCommit, abbrev: "0123f"}, false, "0123f"},
	}
	for _, tt := range tests {
		if got := tt.target.at(head); got != tt.at {
			t.Errorf("%+v.at() = %t, want %t", tt.target, got, tt.at)
		}
		if got := tt.target.label(); got != tt.label {
			t.Errorf("%+v.label() = %q, want %q", tt.target, got, tt.label)
		}
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
