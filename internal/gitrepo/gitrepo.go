// Package gitrepo clones, pulls and creates the config repo with the git
// binary.
//
// maisys relies on git's own sign-in (credential helpers and SSH keys) and
// fills the gaps for HTTPS URLs: an access token from MAISYS_CONFIG_TOKEN,
// handed to git through the environment and never written to disk, or a
// token typed at a prompt when git can't sign in by itself, saved in a
// credentials file that only the config repo's clone uses.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	// ErrNoGit means git isn't installed.
	ErrNoGit = errors.New("git isn't installed")
	// ErrNoFastForward means a pull would need a merge or a rebase.
	ErrNoFastForward = errors.New("can't fast-forward the config repo")
	// ErrNoIdentity means git doesn't know the user's name and email
	// address, so it can't commit.
	ErrNoIdentity = errors.New("git doesn't know your name and email address, which every commit records")
)

// Find returns the path of the git binary.
func Find() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrNoGit
		}
		return "", fmt.Errorf("%w: %w", ErrNoGit, err)
	}
	return path, nil
}

// Options control how git runs.
type Options struct {
	// Git is the git binary, usually from Find. Empty means "git" from PATH.
	Git string
	// Env is extra environment for every git command, for example to keep
	// git away from the user's own config in tests.
	Env []string
	// Auth is how to sign in over HTTPS when git can't by itself.
	Auth Auth
	// Progress receives git's progress output while it clones, pulls and
	// pushes. It may be nil.
	Progress io.Writer
}

// Repo is a git clone.
type Repo struct {
	// Dir is the top of the clone's work tree, as an absolute path.
	Dir string
	Options
}

// Clone clones url into dir. Over HTTPS, it signs in with opts.Auth.Token if
// it's set. Otherwise, if git can't sign in by itself and no credential
// helper is set up, it asks opts.Auth.Prompt for a token, saves it in
// opts.Auth.CredentialsFile and sets up the clone to use it.
func Clone(ctx context.Context, url, dir string, opts Options) (*Repo, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir, Options: opts}
	shown := Redact(url)
	args := append(append([]string{"clone"}, opts.progressFlag()...), "--", url, dir)

	if isHTTPS(url) && opts.Auth.Token != "" {
		c, err := opts.Auth.tokenCall(call{dir: outsideRepos(), args: args, progress: true})
		if err != nil {
			return nil, err
		}
		if _, err := opts.run(ctx, c); err != nil {
			return nil, cloneError(shown, err, func(stderr string) error {
				return tokenRejected(stderr, shown, "the token in MAISYS_CONFIG_TOKEN")
			})
		}
		return r, nil
	}

	_, err = opts.run(ctx, call{dir: outsideRepos(), args: args, progress: true})
	if err == nil {
		return r, nil
	}
	ge, ok := errors.AsType[*gitError](err)
	if !ok || !authFailed(ge.stderr) {
		return nil, cloneError(shown, err, nil)
	}
	if !isHTTPS(url) {
		return nil, otherAuthError(url, ge)
	}
	helper, err := opts.userConfig(ctx, "--get-urlmatch", "credential.helper", url)
	if err != nil {
		return nil, err
	}
	if helper != "" {
		return nil, fmt.Errorf("%w to %s (%s).\nYour git credential helper (%s) didn't have working credentials for it. Sign in with it again, or:\n%s",
			ErrAuth, shown, ge.message(), helper, signInOptions(false))
	}
	if opts.Auth.Prompt == nil || opts.Auth.CredentialsFile == "" {
		return nil, fmt.Errorf("%w to %s (%s).\nTo sign in, do one of these:\n%s", ErrAuth, shown, ge.message(), signInOptions(true))
	}
	return r.cloneWithPrompt(ctx, url, args)
}

// cloneWithPrompt asks for a token, saves it and clones with it, then sets up
// the clone to keep using it.
func (r *Repo) cloneWithPrompt(ctx context.Context, url string, args []string) (*Repo, error) {
	shown := Redact(url)
	file := r.Auth.CredentialsFile
	token, err := r.Auth.Prompt(ctx, shown)
	if err != nil {
		return nil, fmt.Errorf("couldn't clone %s: %w", shown, err)
	}
	if err := SaveToken(file, url, token); err != nil {
		return nil, err
	}
	c := call{dir: outsideRepos(), args: args, config: []string{"credential.helper=", "credential.helper=" + storeHelper(file), useHTTPPath + "=false"}, progress: true}
	if _, err := r.run(ctx, c); err != nil {
		return nil, cloneError(shown, err, func(stderr string) error {
			return tokenRejected(stderr, shown, "that token")
		})
	}
	if err := r.useCredentialsFile(ctx, file); err != nil {
		return nil, fmt.Errorf("cloned %s, but couldn't set it up to use the token saved in %s: %w", shown, file, err)
	}
	return r, nil
}

// cloneError explains a failed clone. rejected, if not nil, turns failures
// that look like a rejected token into a sign-in error.
func cloneError(shown string, err error, rejected func(stderr string) error) error {
	ge, ok := errors.AsType[*gitError](err)
	if !ok {
		return fmt.Errorf("couldn't clone %s: %w", shown, err)
	}
	if rejected != nil && (authFailed(ge.stderr) || repoNotFound(ge.stderr)) {
		return rejected(ge.message())
	}
	return fmt.Errorf("couldn't clone %s: %s", shown, ge.message())
}

// Open returns the clone whose work tree starts at dir.
func Open(ctx context.Context, dir string, opts Options) (*Repo, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s doesn't exist", dir)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s isn't a directory", dir)
	}

	r := &Repo{Dir: dir, Options: opts}
	out, err := r.run(ctx, call{dir: dir, args: []string{"rev-parse", "--is-inside-work-tree", "--show-prefix"}})
	if err != nil {
		if ge, ok := errors.AsType[*gitError](err); ok && strings.Contains(ge.stderr, "not a git repository") {
			return nil, fmt.Errorf("%s isn't a git repository", dir)
		}
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	inside, prefix, _ := strings.Cut(out, "\n")
	switch {
	case strings.TrimSpace(inside) != "true":
		return nil, fmt.Errorf("%s isn't a git work tree", dir)
	case strings.TrimSpace(prefix) != "":
		return nil, fmt.Errorf("%s is inside a git repository, not the top of one", dir)
	}
	return r, nil
}

// Init creates a new repo in dir. Its first branch is called main, unless
// git's init.defaultBranch setting says otherwise.
func Init(ctx context.Context, dir string, opts Options) (*Repo, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	args := []string{"init", "--quiet"}
	branch, err := opts.userConfig(ctx, "--get", "init.defaultBranch")
	if err != nil {
		return nil, err
	}
	if branch == "" {
		args = append(args, "--initial-branch=main")
	}
	r := &Repo{Dir: dir, Options: opts}
	if _, err := r.run(ctx, call{dir: outsideRepos(), args: append(args, "--", dir)}); err != nil {
		return nil, fmt.Errorf("couldn't create a git repository in %s: %w", dir, err)
	}
	return r, nil
}

// RemoteURL returns the URL of the clone's origin, or "" if it has none.
func (r *Repo) RemoteURL(ctx context.Context) (string, error) {
	out, err := r.run(ctx, call{dir: r.Dir, args: []string{"remote", "get-url", "origin"}})
	if err != nil {
		if ge, ok := errors.AsType[*gitError](err); ok && ge.code == 2 && strings.Contains(ge.stderr, "No such remote") {
			return "", nil
		}
		return "", fmt.Errorf("couldn't read the remote of %s: %w", r.Dir, err)
	}
	return strings.TrimSpace(out), nil
}

// Dirty reports whether the clone has uncommitted changes or untracked
// files.
func (r *Repo) Dirty(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, call{dir: r.Dir, args: []string{"--no-optional-locks", "status", "--porcelain", "--untracked-files=normal"}})
	if err != nil {
		return false, fmt.Errorf("couldn't check %s for changes: %w", r.Dir, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// Head returns the abbreviated hash of the commit the clone has checked
// out, or "" if it has no commits yet.
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.run(ctx, call{dir: r.Dir, args: []string{"rev-parse", "--short", "--verify", "--quiet", "HEAD"}})
	if err != nil {
		// With --quiet, a HEAD that doesn't point at a commit yet only
		// exits with code 1.
		if ge, ok := errors.AsType[*gitError](err); ok && ge.code == 1 && strings.TrimSpace(ge.stderr) == "" {
			return "", nil
		}
		return "", fmt.Errorf("couldn't read the commit of %s: %w", r.Dir, err)
	}
	return strings.TrimSpace(out), nil
}

// Pull fast-forwards the clone to its upstream branch. It fails with
// ErrNoFastForward if that would need a merge, and with ErrAuth if git can't
// sign in, for example because the saved token has expired. If a token
// typed at the prompt was saved for the clone, and it's rejected, Pull asks
// r.Auth.Prompt for a new one, saves it in its place and tries again.
func (r *Repo) Pull(ctx context.Context) error {
	url, err := r.RemoteURL(ctx)
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("the config repo in %s has no remote to pull from", r.Dir)
	}

	c := call{dir: r.Dir, args: append([]string{"pull", "--ff-only"}, r.progressFlag()...), progress: true}
	withToken := isHTTPS(url) && r.Auth.Token != ""
	if withToken {
		if c, err = r.Auth.tokenCall(c); err != nil {
			return err
		}
	}
	_, err = r.run(ctx, c)
	if err == nil {
		return nil
	}
	if ge, ok := errors.AsType[*gitError](err); ok && authFailed(ge.stderr) && !withToken && isHTTPS(url) && r.Auth.Prompt != nil {
		if file := r.credentialsFile(ctx); file != "" {
			return r.pullWithNewToken(ctx, url, file, c)
		}
	}
	return r.pullError(ctx, url, err, withToken)
}

// pullWithNewToken asks for a token in place of the one saved in file,
// which the clone signs in with and which was rejected, saves it, and pulls
// again with c.
func (r *Repo) pullWithNewToken(ctx context.Context, url, file string, c call) error {
	shown := Redact(url)
	if r.Auth.PromptOut != nil {
		_, _ = fmt.Fprintf(r.Auth.PromptOut, "The token saved in %s for %s was rejected: it may have expired or been revoked.\n", file, shown)
	}
	token, err := r.Auth.Prompt(ctx, shown)
	if err != nil {
		return fmt.Errorf("couldn't pull the config repo in %s: %w", r.Dir, err)
	}
	if err := SaveToken(file, url, token); err != nil {
		return err
	}
	_, err = r.run(ctx, c)
	if err == nil {
		return nil
	}
	if ge, ok := errors.AsType[*gitError](err); ok && (authFailed(ge.stderr) || repoNotFound(ge.stderr)) {
		return fmt.Errorf("%w\n%s", tokenRejected(ge.message(), shown, "that token"), localHint)
	}
	return r.pullError(ctx, url, err, false)
}

// pullError explains a failed pull.
func (r *Repo) pullError(ctx context.Context, url string, err error, withToken bool) error {
	ge, ok := errors.AsType[*gitError](err)
	switch {
	case !ok:
		return fmt.Errorf("couldn't pull the config repo in %s: %w", r.Dir, err)
	case strings.Contains(ge.stderr, "Not possible to fast-forward"):
		return fmt.Errorf("%w in %s: both it and %s have new commits. Merge or rebase them by hand, or run \"maisys sync --local\" to use the clone as it is",
			ErrNoFastForward, r.Dir, Redact(url))
	case authFailed(ge.stderr) || (withToken && repoNotFound(ge.stderr)):
		return r.pullAuthError(ctx, url, ge, withToken)
	}
	return fmt.Errorf("couldn't pull the config repo in %s: %s", r.Dir, ge.message())
}

// CommitAll commits every change in the work tree, including new files.
func (r *Repo) CommitAll(ctx context.Context, message string) error {
	if _, err := r.run(ctx, call{dir: r.Dir, args: []string{"add", "--all"}}); err != nil {
		return fmt.Errorf("couldn't add the files in %s to git: %w", r.Dir, err)
	}
	_, err := r.run(ctx, call{dir: r.Dir, args: []string{"commit", "--quiet", "--message", message}})
	if err == nil {
		return nil
	}
	if ge, ok := errors.AsType[*gitError](err); ok && noIdentity(ge.stderr) {
		return identityError()
	}
	return fmt.Errorf("couldn't commit in %s: %w", r.Dir, err)
}

// CheckIdentity checks that git knows the name and email address to record
// in a commit, as CommitAll needs, so a commit can't fail for that after
// other work is done. It fails with ErrNoIdentity if git doesn't.
func CheckIdentity(ctx context.Context, opts Options) error {
	for _, ident := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		_, err := opts.run(ctx, call{dir: outsideRepos(), args: []string{"var", ident}})
		if err == nil {
			continue
		}
		if ge, ok := errors.AsType[*gitError](err); ok && noIdentity(ge.stderr) {
			return identityError()
		}
		return fmt.Errorf("couldn't read git's settings: %w", err)
	}
	return nil
}

// identityError is the error for a commit git can't make, because it
// doesn't know who's making it.
func identityError() error {
	return fmt.Errorf(`%w. Set them with:
  git config --global user.name "Your Name"
  git config --global user.email "you@example.com"`, ErrNoIdentity)
}

// SetRemote sets the URL of the clone's origin, adding origin if it's
// missing.
func (r *Repo) SetRemote(ctx context.Context, url string) error {
	current, err := r.RemoteURL(ctx)
	if err != nil {
		return err
	}
	verb := "add"
	if current != "" {
		verb = "set-url"
	}
	if _, err := r.run(ctx, call{dir: r.Dir, args: []string{"remote", verb, "origin", url}}); err != nil {
		return fmt.Errorf("couldn't set the remote of %s: %w", r.Dir, err)
	}
	return nil
}

// Push pushes the current branch to origin and makes it the branch's
// upstream. Over HTTPS, it signs in with r.Auth.Token if it's set.
func (r *Repo) Push(ctx context.Context) error {
	url, err := r.RemoteURL(ctx)
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("the repo in %s has no remote to push to", r.Dir)
	}

	c := call{dir: r.Dir, args: append(append([]string{"push"}, r.progressFlag()...), "--set-upstream", "origin", "HEAD"), progress: true}
	withToken := isHTTPS(url) && r.Auth.Token != ""
	if withToken {
		if c, err = r.Auth.tokenCall(c); err != nil {
			return err
		}
	}
	_, err = r.run(ctx, c)
	if err == nil {
		return nil
	}
	ge, ok := errors.AsType[*gitError](err)
	if ok && (authFailed(ge.stderr) || (withToken && repoNotFound(ge.stderr))) {
		fix := "Set up git's sign-in for it (a credential helper or an SSH key), or set MAISYS_CONFIG_TOKEN to an access token that can write to the repo"
		if withToken {
			fix = "The token in MAISYS_CONFIG_TOKEN was rejected, or can't write to the repo. Set it to an access token that can write to the repo, or unset it to use git's own sign-in"
		}
		return fmt.Errorf("%w to %s to push (%s).\nPushing needs write access. %s", ErrAuth, Redact(url), ge.message(), fix)
	}
	if ok {
		return fmt.Errorf("couldn't push %s to %s: %s", r.Dir, Redact(url), ge.message())
	}
	return fmt.Errorf("couldn't push %s to %s: %w", r.Dir, Redact(url), err)
}

func (o *Options) progressFlag() []string {
	if o.Progress == nil {
		return nil
	}
	return []string{"--progress"}
}

// userConfig returns a value from the user's git config (system and global),
// or "" if it isn't set. args are git config's arguments.
func (o *Options) userConfig(ctx context.Context, args ...string) (string, error) {
	out, err := o.run(ctx, call{dir: outsideRepos(), args: append([]string{"config"}, args...)})
	if err != nil {
		if ge, ok := errors.AsType[*gitError](err); ok && ge.code == 1 {
			return "", nil
		}
		return "", fmt.Errorf("couldn't read git's settings: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// outsideRepos is where git runs when it isn't working in a repo, so that it
// never reads the config of whichever repo maisys was started in.
func outsideRepos() string {
	return os.TempDir()
}

// call is one git command.
type call struct {
	dir      string
	args     []string
	config   []string // name=value settings for this command only
	env      []string // environment for this command only
	progress bool     // pass git's progress output on to Options.Progress
}

// subcommand returns the git command c runs, such as "clone".
func (c *call) subcommand() string {
	for _, arg := range c.args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// repoEnv are environment variables that point git at a particular repo.
// They're dropped so git always works on the repo it's run in, even when
// maisys runs from a git hook.
var repoEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_PREFIX",
}

// environ returns the environment for a git command.
func (o *Options) environ(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(o.Env)+len(extra)+3)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !containsFold(repoEnv, name) {
			env = append(env, kv)
		}
	}
	env = append(env, o.Env...)
	env = append(env,
		// git never asks for a username or password itself: maisys does.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		// Untranslated messages, so errors can be recognised.
		"LC_ALL=C",
	)
	return append(env, extra...)
}

// run runs a git command and returns its standard output.
func (o *Options) run(ctx context.Context, c call) (string, error) {
	git := o.Git
	if git == "" {
		git = "git"
	}
	var args []string
	for _, kv := range c.config {
		args = append(args, "-c", kv)
	}
	args = append(args, c.args...)

	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir = c.dir
	cmd.Env = o.environ(c.env)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var progress *progressWriter
	if c.progress && o.Progress != nil {
		progress = &progressWriter{w: o.Progress}
		cmd.Stderr = io.MultiWriter(&stderr, progress)
	}

	err := cmd.Run()
	if progress != nil {
		progress.flush()
	}
	if err == nil {
		return stdout.String(), nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("git %s: %w", c.subcommand(), ctx.Err())
	}
	if pe, ok := errors.AsType[*fs.PathError](err); ok && pe.Op == "chdir" {
		return "", err
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %w", ErrNoGit, err)
	}
	ge := &gitError{subcommand: c.subcommand(), stdout: stdout.String(), stderr: stderr.String(), err: err, code: -1}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		ge.code = exitErr.ExitCode()
	}
	return "", ge
}

// gitError is a git command that failed.
type gitError struct {
	subcommand     string
	stdout, stderr string
	code           int // exit code, or -1
	err            error
}

func (e *gitError) Error() string {
	return "git " + e.subcommand + ": " + e.message()
}

func (e *gitError) Unwrap() error { return e.err }

// message returns the line of git's output that best explains the failure.
func (e *gitError) message() string {
	lines := strings.FieldsFunc(e.stderr, func(r rune) bool { return r == '\n' || r == '\r' })
	for _, line := range slices.Backward(lines) {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"fatal: ", "error: "} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				return rest
			}
		}
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "hint:") && !strings.Contains(line, "% (") {
			return line
		}
	}
	lines = strings.Split(strings.TrimSpace(e.stdout), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return e.err.Error()
}

// progressWriter passes git's progress output on, leaving out the error
// lines maisys reports itself.
type progressWriter struct {
	w       io.Writer
	pending []byte
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.pending = append(p.pending, b...)
	for {
		i := bytes.IndexAny(p.pending, "\r\n")
		if i < 0 {
			return len(b), nil
		}
		p.write(p.pending[:i+1])
		p.pending = p.pending[i+1:]
	}
}

func (p *progressWriter) flush() {
	p.write(p.pending)
	p.pending = nil
}

func (p *progressWriter) write(line []byte) {
	if len(line) == 0 || bytes.HasPrefix(line, []byte("fatal: ")) || bytes.HasPrefix(line, []byte("error: ")) {
		return
	}
	// Progress is only informational, so a failed write doesn't stop git.
	_, _ = p.w.Write(line)
}

func noIdentity(stderr string) bool {
	for _, m := range []string{"identity unknown", "empty ident name", "auto-detection is disabled", "unable to auto-detect email"} {
		if strings.Contains(stderr, m) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
