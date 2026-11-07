package gitrepo

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// testToken has characters that git's store helper percent-encodes.
const testToken = "s3cret+tok/en"

// prompt is a fake Auth.Prompt that records the URLs it's asked about.
type prompt struct {
	answer string
	err    error
	urls   []string
}

func (p *prompt) ask(_ context.Context, url string) (string, error) {
	p.urls = append(p.urls, url)
	return p.answer, p.err
}

// noPrompt is an Auth.Prompt that fails the test if it's called.
func noPrompt(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	return func(_ context.Context, url string) (string, error) {
		t.Errorf("asked for a token for %s", url)
		return "", errors.New("unexpected prompt")
	}
}

func cloneDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "maisys-config")
}

func TestFind(t *testing.T) {
	if _, err := exec.LookPath("git"); err == nil {
		if got, err := Find(); err != nil || got == "" {
			t.Errorf("Find() = %q, %v; want git's path", got, err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Find(); !errors.Is(err, ErrNoGit) {
		t.Errorf("Find() without git in PATH: error = %v, want ErrNoGit", err)
	}
}

func TestRunErrors(t *testing.T) {
	sb := newSandbox(t)
	missing := filepath.Join(t.TempDir(), "missing")

	r := &Repo{Dir: missing, Options: sb.opts}
	_, err := r.Dirty(t.Context())
	assertErrorContains(t, err, missing)
	if errors.Is(err, ErrNoGit) {
		t.Errorf("Dirty() in a missing directory: error = %v, want one that isn't ErrNoGit", err)
	}

	r = initWithCommit(t, sb)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Pull(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Pull() after cancelling: error = %v, want context.Canceled", err)
	}

	r.Git = filepath.Join(missing, "git")
	if _, err := r.Dirty(t.Context()); !errors.Is(err, ErrNoGit) {
		t.Errorf("Dirty() with a missing git: error = %v, want ErrNoGit", err)
	}
}

func TestClonePublicRepo(t *testing.T) {
	sb := newSandbox(t)
	srv := newGitServer(t, sb)
	var progress bytes.Buffer
	opts := sb.opts
	opts.Auth = Auth{CredentialsFile: sb.credentialsFile(), Prompt: noPrompt(t)}
	opts.Progress = &progress
	dir := cloneDir(t)

	url := srv.publicURL("config.git")
	r, err := Clone(t.Context(), url, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(dir, "maisys.toml"), "config_version = 1\n")
	if got, err := r.RemoteURL(t.Context()); err != nil || got != url {
		t.Errorf("RemoteURL() = %q, %v; want %q", got, err, url)
	}
	if !strings.Contains(progress.String(), "Cloning into") {
		t.Errorf("progress output = %q, want git's progress", progress.String())
	}
	assertNotExist(t, sb.credentialsFile())
}

func TestCloneWithToken(t *testing.T) {
	sb := newSandbox(t)
	srv := newGitServer(t, sb)
	// A helper that would save the token, if maisys didn't turn it off.
	sb.setGlobal("credential.helper", "store --file "+shellQuote(filepath.ToSlash(filepath.Join(sb.home, "user-credentials"))))
	opts := sb.opts
	opts.Auth = Auth{Token: testToken, CredentialsFile: sb.credentialsFile(), Prompt: noPrompt(t)}
	dir := cloneDir(t)

	r, err := Clone(t.Context(), srv.privateURL("config.git"), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(dir, "maisys.toml"), "config_version = 1\n")
	assertNotExist(t, sb.credentialsFile())
	assertTokenNotSaved(t, testToken, sb.home, dir)

	// Pulls sign in with the token too.
	sb.commitUpstream(filepath.Join(srv.root, "config.git"), "base.toml", "[packages]\n")
	if err := r.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(dir, "base.toml"), "[packages]\n")
	assertTokenNotSaved(t, testToken, sb.home, dir)

	// Without the token, the clone can't sign in: nothing kept it.
	r.Auth = Auth{}
	if err := r.Pull(t.Context()); !errors.Is(err, ErrAuth) {
		t.Errorf("Pull() without the token: error = %v, want ErrAuth", err)
	}
}

func TestCloneWithPrompt(t *testing.T) {
	sb := newSandbox(t)
	srv := newGitServer(t, sb)
	r, p := cloneWithPrompt(t, sb, srv)
	file := sb.credentialsFile()

	if want := []string{srv.privateURL("config.git")}; !slices.Equal(p.urls, want) {
		t.Errorf("asked for a token for %q, want %q", p.urls, want)
	}
	assertContent(t, filepath.Join(r.Dir, "maisys.toml"), "config_version = 1\n")

	// The token is saved for the server's host, readable only by the user.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	if len(lines) != 1 || !sameHost(lines[0], srv.host()) ||
		!strings.HasPrefix(strings.ToLower(lines[0]), "https://maisys:"+escapeCredential(testToken)+"@") {
		t.Errorf("%s contains %q, want one line with the token for %s", file, data, srv.host())
	}
	if runtime.GOOS != "windows" {
		assertMode(t, file, 0o600)
		assertMode(t, filepath.Dir(file), 0o700)
	}

	// Only the clone is set up to use it.
	helpers := strings.Split(sb.git(r.Dir, "config", "--local", "--null", "--get-all", "credential.helper"), "\x00")
	if want := []string{"", "store --file " + shellQuote(filepath.ToSlash(file)), ""}; !slices.Equal(helpers, want) {
		t.Errorf("the clone's credential.helper = %q, want %q", helpers, want)
	}
	if global, err := os.ReadFile(filepath.Join(sb.home, "gitconfig")); err != nil || len(global) != 0 {
		t.Errorf("the global git config = %q, %v; want it empty", global, err)
	}

	// A later pull signs in with the saved token without asking.
	sb.commitUpstream(filepath.Join(srv.root, "config.git"), "base.toml", "[packages]\n")
	r.Auth.Prompt = noPrompt(t)
	if err := r.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(r.Dir, "base.toml"), "[packages]\n")
}

func TestPullWithExpiredToken(t *testing.T) {
	sb := newSandbox(t)
	srv := newGitServer(t, sb)
	r, _ := cloneWithPrompt(t, sb, srv)
	r.Auth.Prompt = noPrompt(t)
	srv.setToken("new-token")

	err := r.Pull(t.Context())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Pull() error = %v, want ErrAuth", err)
	}
	assertErrorContains(t, err, "to pull the config repo", "token saved in "+sb.credentialsFile(),
		"MAISYS_CONFIG_TOKEN", `"maisys sync --local"`)

	// MAISYS_CONFIG_TOKEN takes the place of the saved token.
	r.Auth.Token = "new-token"
	if err := r.Pull(t.Context()); err != nil {
		t.Errorf("Pull() with a new MAISYS_CONFIG_TOKEN: %v", err)
	}
}

// cloneWithPrompt clones the server's private repo, typing testToken at the
// prompt.
func cloneWithPrompt(t *testing.T, sb *sandbox, srv *gitServer) (*Repo, *prompt) {
	t.Helper()
	p := &prompt{answer: testToken}
	opts := sb.opts
	opts.Auth = Auth{CredentialsFile: sb.credentialsFile(), Prompt: p.ask}
	r, err := Clone(t.Context(), srv.privateURL("config.git"), cloneDir(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return r, p
}

func TestCloneFailures(t *testing.T) {
	tests := []struct {
		name      string
		repo      string // under /private/, unless public
		public    bool
		token     string // MAISYS_CONFIG_TOKEN
		terminal  bool   // a prompt is available
		answer    string
		answerErr error
		helper    bool // the user has a credential helper
		prompts   int
		auth      bool // the error wraps ErrAuth
		want      []string
	}{
		{
			name:     "wrong token typed at the prompt",
			terminal: true,
			answer:   "wrong",
			prompts:  1,
			auth:     true,
			want:     []string{"with that token", "needs read access", "Contents: read"},
		},
		{
			name:     "wrong MAISYS_CONFIG_TOKEN",
			token:    "wrong",
			terminal: true,
			auth:     true,
			want:     []string{"with the token in MAISYS_CONFIG_TOKEN", "needs read access"},
		},
		{
			name: "no token and no terminal",
			auth: true,
			want: []string{"git couldn't sign in", "set MAISYS_CONFIG_TOKEN", "run maisys in a terminal", "SSH URL"},
		},
		{
			name:     "credential helper without credentials",
			terminal: true,
			helper:   true,
			auth:     true,
			want:     []string{"credential helper (store --file", "set MAISYS_CONFIG_TOKEN"},
		},
		{
			name:      "nothing typed at the prompt",
			terminal:  true,
			answerErr: errors.New("no token entered"),
			prompts:   1,
			want:      []string{"no token entered"},
		},
		{
			name:     "repo doesn't exist",
			repo:     "missing.git",
			public:   true,
			terminal: true,
			want:     []string{"couldn't clone", "not found"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := newSandbox(t)
			srv := newGitServer(t, sb)
			if tt.helper {
				sb.setGlobal("credential.helper", "store --file "+shellQuote(filepath.ToSlash(filepath.Join(sb.home, "other-credentials"))))
			}
			p := &prompt{answer: tt.answer, err: tt.answerErr}
			opts := sb.opts
			opts.Auth = Auth{Token: tt.token, CredentialsFile: sb.credentialsFile()}
			if tt.terminal {
				opts.Auth.Prompt = p.ask
			}
			repo := cmp.Or(tt.repo, "config.git")
			url := srv.privateURL(repo)
			if tt.public {
				url = srv.publicURL(repo)
			}
			dir := cloneDir(t)

			_, err := Clone(t.Context(), url, dir, opts)
			assertErrorContains(t, err, tt.want...)
			if errors.Is(err, ErrAuth) != tt.auth {
				t.Errorf("errors.Is(err, ErrAuth) = %v, want %v", !tt.auth, tt.auth)
			}
			if len(p.urls) != tt.prompts {
				t.Errorf("asked for a token %d times, want %d", len(p.urls), tt.prompts)
			}
			assertNotExist(t, dir)
		})
	}
}

func TestPullFastForward(t *testing.T) {
	sb := newSandbox(t)
	bare := filepath.Join(t.TempDir(), "config.git")
	sb.bareRepo(bare)
	r, err := Clone(t.Context(), bare, cloneDir(t), sb.opts)
	if err != nil {
		t.Fatal(err)
	}

	sb.commitUpstream(bare, "base.toml", "[packages]\n")
	if err := r.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(r.Dir, "base.toml"), "[packages]\n")
}

func TestPullNotFastForward(t *testing.T) {
	sb := newSandbox(t)
	bare := filepath.Join(t.TempDir(), "config.git")
	sb.bareRepo(bare)
	r, err := Clone(t.Context(), bare, cloneDir(t), sb.opts)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(r.Dir, "local.toml"), "local = true\n")
	if err := r.CommitAll(t.Context(), "A local change"); err != nil {
		t.Fatal(err)
	}
	sb.commitUpstream(bare, "base.toml", "[packages]\n")

	err = r.Pull(t.Context())
	if !errors.Is(err, ErrNoFastForward) {
		t.Fatalf("Pull() error = %v, want ErrNoFastForward", err)
	}
	assertErrorContains(t, err, "Merge or rebase", `"maisys sync --local"`)
	assertNotExist(t, filepath.Join(r.Dir, "base.toml"))
}

func TestPullWithoutRemote(t *testing.T) {
	sb := newSandbox(t)
	r, err := Init(t.Context(), filepath.Join(t.TempDir(), "config"), sb.opts)
	if err != nil {
		t.Fatal(err)
	}
	assertErrorContains(t, r.Pull(t.Context()), "has no remote to pull from")
}

func TestDirty(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
		want   bool
	}{
		{"clean", func(*testing.T, string) {}, false},
		{"modified file", func(t *testing.T, dir string) {
			t.Helper()
			writeFile(t, filepath.Join(dir, "maisys.toml"), "config_version = 2\n")
		}, true},
		{"untracked file", func(t *testing.T, dir string) {
			t.Helper()
			writeFile(t, filepath.Join(dir, "profiles", "base.toml"), "")
		}, true},
		{"deleted file", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Remove(filepath.Join(dir, "maisys.toml")); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := newSandbox(t)
			r := initWithCommit(t, sb)
			tt.change(t, r.Dir)
			got, err := r.Dirty(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Dirty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRemoteURL(t *testing.T) {
	sb := newSandbox(t)
	r := initWithCommit(t, sb)
	assertRemoteURL(t, r, "")

	for _, url := range []string{"https://example.com/me/config.git", "git@example.com:me/config.git"} {
		if err := r.SetRemote(t.Context(), url); err != nil {
			t.Fatal(err)
		}
		assertRemoteURL(t, r, url)
	}
}

func TestCommitAllAndPush(t *testing.T) {
	sb := newSandbox(t)
	r, err := Init(t.Context(), filepath.Join(t.TempDir(), "config"), sb.opts)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "maisys.toml"), "config_version = 1\n")
	writeFile(t, filepath.Join(r.Dir, "profiles", "base.toml"), "")
	if err := r.CommitAll(t.Context(), "Create the config repo"); err != nil {
		t.Fatal(err)
	}
	if dirty, err := r.Dirty(t.Context()); err != nil || dirty {
		t.Errorf("Dirty() after CommitAll = %v, %v; want false", dirty, err)
	}
	if got := sb.git(r.Dir, "log", "--format=%s"); got != "Create the config repo" {
		t.Errorf("commits = %q, want the one commit", got)
	}

	bare := filepath.Join(t.TempDir(), "config.git")
	sb.git("", "init", "--quiet", "--bare", "--initial-branch=main", bare)
	if err := r.SetRemote(t.Context(), bare); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(t.Context()); err != nil {
		t.Fatal(err)
	}
	if local, remote := sb.git(r.Dir, "rev-parse", "HEAD"), sb.git(bare, "rev-parse", "main"); local != remote {
		t.Errorf("origin's main is %s, want %s", remote, local)
	}

	// The push made origin's branch the upstream, so pulls work.
	sb.commitUpstream(bare, "fedora.toml", "")
	if err := r.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertContent(t, filepath.Join(r.Dir, "fedora.toml"), "")
}

func TestPushWithoutRemote(t *testing.T) {
	sb := newSandbox(t)
	r := initWithCommit(t, sb)
	assertErrorContains(t, r.Push(t.Context()), "has no remote to push to")
}

func TestCommitAllWithoutIdentity(t *testing.T) {
	sb := newSandbox(t)
	sb.setGlobal("user.useConfigOnly", "true")
	opts := sb.opts
	opts.Env = append(slices.Clip(opts.Env), "GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=", "EMAIL=")
	r, err := Init(t.Context(), filepath.Join(t.TempDir(), "config"), opts)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "maisys.toml"), "config_version = 1\n")

	err = r.CommitAll(t.Context(), "Create the config repo")
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("CommitAll() error = %v, want ErrNoIdentity", err)
	}
	assertErrorContains(t, err, `git config --global user.name "Your Name"`, `git config --global user.email "you@example.com"`)
}

func TestInitBranch(t *testing.T) {
	tests := []struct {
		defaultBranch string // init.defaultBranch
		want          string
	}{
		{"", "main"},
		{"trunk", "trunk"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			sb := newSandbox(t)
			if tt.defaultBranch != "" {
				sb.setGlobal("init.defaultBranch", tt.defaultBranch)
			}
			r, err := Init(t.Context(), filepath.Join(t.TempDir(), "config"), sb.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := sb.git(r.Dir, "symbolic-ref", "--short", "HEAD"); got != tt.want {
				t.Errorf("branch = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpen(t *testing.T) {
	sb := newSandbox(t)
	r := initWithCommit(t, sb)
	plain := t.TempDir()
	file := filepath.Join(plain, "file")
	writeFile(t, file, "")
	writeFile(t, filepath.Join(r.Dir, "profiles", "base.toml"), "")

	tests := []struct {
		dir  string
		want string // part of the error; "" for none
	}{
		{r.Dir, ""},
		{filepath.Join(r.Dir, "profiles"), "is inside a git repository, not the top of one"},
		{filepath.Join(r.Dir, ".git"), "isn't a git work tree"},
		{plain, "isn't a git repository"},
		{file, "isn't a directory"},
		{filepath.Join(plain, "missing"), "doesn't exist"},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.dir), func(t *testing.T) {
			got, err := Open(t.Context(), tt.dir, sb.opts)
			if tt.want == "" {
				if err != nil || got.Dir != tt.dir {
					t.Errorf("Open() = %v, %v; want the clone", got, err)
				}
				return
			}
			assertErrorContains(t, err, tt.dir+" "+tt.want)
		})
	}
}

// TestRepoEnvironmentIgnored checks git works on the repo it's run in, even
// when the environment points it at another one, as in a git hook.
func TestRepoEnvironmentIgnored(t *testing.T) {
	sb := newSandbox(t)
	other := initWithCommit(t, sb)
	r := initWithCommit(t, sb)
	if err := r.SetRemote(t.Context(), "https://example.com/me/config.git"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other.Dir, ".git"))
	t.Setenv("GIT_WORK_TREE", other.Dir)
	assertRemoteURL(t, r, "https://example.com/me/config.git")
}

// initWithCommit creates a repo with one commit, of maisys.toml.
func initWithCommit(t *testing.T, sb *sandbox) *Repo {
	t.Helper()
	r, err := Init(t.Context(), filepath.Join(t.TempDir(), "config"), sb.opts)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "maisys.toml"), "config_version = 1\n")
	if err := r.CommitAll(t.Context(), "Create the config repo"); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSaveToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "maisys", "git-credentials")
	steps := []struct {
		url, token string
		want       string
	}{
		{
			"https://github.com/me/config.git", "first",
			"https://maisys:first@github.com\n",
		},
		{
			"https://gitlab.example.com:8443/me/config.git", "other",
			"https://maisys:first@github.com\nhttps://maisys:other@gitlab.example.com:8443\n",
		},
		{
			"https://GitHub.com/me/config.git", "sec+ret/=@",
			"https://maisys:other@gitlab.example.com:8443\nhttps://maisys:sec%2bret%2f%3d%40@GitHub.com\n",
		},
		// Replaces the line as git's store helper writes it (see below).
		{
			"https://user@gitlab.example.com:8443/me/config.git", "third",
			"https://maisys:sec%2bret%2f%3d%40@GitHub.com\nhttps://user:third@gitlab.example.com:8443\n",
		},
	}
	for i, s := range steps {
		if err := SaveToken(file, s.url, s.token); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		assertContent(t, file, s.want)
		if i == 2 {
			// Rewrite the file the way git's store helper would, with
			// the port separator percent-encoded.
			writeFile(t, file, strings.Replace(s.want, "gitlab.example.com:8443", "gitlab.example.com%3a8443", 1))
		}
	}
	if runtime.GOOS != "windows" {
		assertMode(t, file, 0o600)
		assertMode(t, filepath.Dir(file), 0o700)
	}
	assertDirEntries(t, filepath.Dir(file), "git-credentials")

	failures := []struct {
		url, token, want string
	}{
		{"git@github.com:me/config.git", "token", "isn't an HTTPS URL"},
		{"http://example.com/config.git", "token", "isn't an HTTPS URL"},
		{"https://example.com/config.git", "", "the access token is empty"},
		{"https://example.com/config.git", "two words", "can't contain spaces or line breaks"},
		{"https://example.com/config.git", "line\nbreak", "can't contain spaces or line breaks"},
	}
	for _, f := range failures {
		assertErrorContains(t, SaveToken(file, f.url, f.token), f.want)
	}
}

func TestAuthFailed(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", true},
		{"fatal: could not read Password for 'https://me@github.com': terminal prompts disabled", true},
		{"remote: Invalid username or token. Password authentication is not supported for Git operations.\nfatal: Authentication failed for 'https://github.com/me/config.git/'", true},
		{"remote: HTTP Basic: Access denied.\nfatal: Authentication failed for 'https://gitlab.com/me/config.git/'", true},
		{"fatal: unable to access 'https://example.com/config.git/': The requested URL returned error: 403", true},
		{"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.", true},
		{"remote: Repository not found.\nfatal: repository 'https://github.com/me/missing.git/' not found", false},
		{"fatal: unable to access 'https://example.com/': Could not resolve host: example.com", false},
	}
	for _, tt := range tests {
		if got := authFailed(tt.stderr); got != tt.want {
			t.Errorf("authFailed(%q) = %v, want %v", tt.stderr, got, tt.want)
		}
	}
}

func TestURLKinds(t *testing.T) {
	tests := []struct {
		url        string
		https, ssh bool
		redacted   string
	}{
		{"https://github.com/me/config.git", true, false, "https://github.com/me/config.git"},
		{"https://me:ghp_secret@github.com/me/config.git", true, false, "https://xxxxx@github.com/me/config.git"},
		{"https://ghp_secret@github.com/me/config.git", true, false, "https://xxxxx@github.com/me/config.git"},
		{"http://example.com/config.git", false, false, "http://example.com/config.git"},
		{"git@github.com:me/config.git", false, true, "git@github.com:me/config.git"},
		{"ssh://git@github.com/me/config.git", false, true, "ssh://xxxxx@github.com/me/config.git"},
		{"/home/me/config.git", false, false, "/home/me/config.git"},
		{`C:\Users\me\config.git`, false, false, `C:\Users\me\config.git`},
		{"file:///home/me/config.git", false, false, "file:///home/me/config.git"},
	}
	for _, tt := range tests {
		if got := isHTTPS(tt.url); got != tt.https {
			t.Errorf("isHTTPS(%q) = %v, want %v", tt.url, got, tt.https)
		}
		if got := isSSH(tt.url); got != tt.ssh {
			t.Errorf("isSSH(%q) = %v, want %v", tt.url, got, tt.ssh)
		}
		if got := redact(tt.url); got != tt.redacted {
			t.Errorf("redact(%q) = %q, want %q", tt.url, got, tt.redacted)
		}
	}
}

func TestGitErrorMessage(t *testing.T) {
	tests := []struct {
		stdout, stderr string
		want           string
	}{
		{"", "Cloning into 'x'...\nremote: Repository not found.\nfatal: repository 'https://example.com/x/' not found\n", "repository 'https://example.com/x/' not found"},
		{"", "To /tmp/config.git\n ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs to '/tmp/config.git'\nhint: Updates were rejected\n", "failed to push some refs to '/tmp/config.git'"},
		{"", "There is no tracking information for the current branch.\nPlease specify which branch you want to merge with.\n", "There is no tracking information for the current branch."},
		{"On branch main\nnothing to commit, working tree clean\n", "", "nothing to commit, working tree clean"},
		{"", "", "exit status 1"},
	}
	for _, tt := range tests {
		e := &gitError{subcommand: "test", stdout: tt.stdout, stderr: tt.stderr, err: errors.New("exit status 1")}
		if got := e.message(); got != tt.want {
			t.Errorf("message() for %q = %q, want %q", tt.stderr+tt.stdout, got, tt.want)
		}
	}
}

func TestProgressWriter(t *testing.T) {
	var out bytes.Buffer
	p := &progressWriter{w: &out}
	for _, chunk := range []string{
		"Cloning into 'config'...\nReceiving obj", "ects:  50%\rReceiving objects: 100% (3/3), done.\n",
		"fatal: could not read Username for 'https://example.com': terminal prompts disabled\n",
		"error: something\nremote: Total 3", " (delta 0)",
	} {
		if n, err := p.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write() = %d, %v; want %d, nil", n, err, len(chunk))
		}
	}
	p.flush()
	want := "Cloning into 'config'...\nReceiving objects:  50%\rReceiving objects: 100% (3/3), done.\nremote: Total 3 (delta 0)"
	if out.String() != want {
		t.Errorf("progress = %q, want %q", out.String(), want)
	}
}

// assertTokenNotSaved checks no file under dirs contains token, as it is or
// percent-encoded.
func assertTokenNotSaved(t *testing.T, token string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lower := bytes.ToLower(data)
			if bytes.Contains(data, []byte(token)) || bytes.Contains(lower, []byte(strings.ToLower(escapeCredential(token)))) {
				t.Errorf("%s contains the token", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func assertRemoteURL(t *testing.T, r *Repo, want string) {
	t.Helper()
	if got, err := r.RemoteURL(t.Context()); err != nil || got != want {
		t.Errorf("RemoteURL() = %q, %v; want %q", got, err, want)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s contains %q, want %q", path, got, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), want)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (%v), want it not to", path, err)
	}
}

// assertDirEntries checks dir contains exactly the named entries, so no
// temporary files are left behind.
func assertDirEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s contains %v, want %v", dir, got, want)
	}
}

func assertErrorContains(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want one containing %q", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error = %q, want it to contain %q", err, w)
		}
	}
}
