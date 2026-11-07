package checkout

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

// gitRunner runs git for checkouts.
type gitRunner struct {
	runner system.Runner
	git    string
	env    []string
}

// git returns the gitRunner for the checkout.
func (c *Checkout) git(rt *resource.Runtime) gitRunner {
	return gitRunner{runner: rt.Runner, git: c.Git, env: c.Env}
}

// run runs git with args. With a checkout's directory, git works on that
// checkout whatever the environment says, as when maisys runs from a git
// hook; without one, it runs where it can't find a repo of its own. git
// never asks for credentials, and commands that reach a repo use no
// credential helper. allow lists the exit codes other than 0 that aren't
// errors.
func (g gitRunner) run(ctx context.Context, dir string, allow []int, args ...string) (system.Result, error) {
	var full []string
	if sub := subcommand(args); sub == "fetch" || sub == "ls-remote" {
		full = append(full, "-c", "credential.helper=")
	}
	full = append(full, args...)
	env := append(append([]string(nil), g.env...), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	workDir := os.TempDir()
	if dir != "" {
		workDir = dir
		env = append(env, "GIT_DIR="+filepath.Join(dir, ".git"), "GIT_WORK_TREE="+dir)
	}
	return g.runner.Run(ctx, system.Cmd{
		Name:      cmp.Or(g.git, "git"),
		Args:      full,
		Dir:       workDir,
		Env:       env,
		Label:     "git " + subcommand(args),
		AllowExit: allow,
	})
}

// subcommand returns the git command args run, such as "fetch".
func subcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// revParse returns the full hash of the commit rev names in the checkout in
// dir, and whether there is one.
func (g gitRunner) revParse(ctx context.Context, dir, rev string) (string, bool, error) {
	res, err := g.run(ctx, dir, []int{1}, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || res.ExitCode != 0 {
		return "", false, err
	}
	return strings.TrimSpace(string(res.Stdout)), true, nil
}

// shallow reports whether the checkout in dir is shallow.
func (g gitRunner) shallow(ctx context.Context, dir string) (bool, error) {
	res, err := g.run(ctx, dir, nil, "rev-parse", "--is-shallow-repository")
	return strings.TrimSpace(string(res.Stdout)) == "true", err
}

// checkout checks out commit, detached, in the checkout in dir, discarding
// local changes to tracked files.
func (g gitRunner) checkout(ctx context.Context, dir, commit string) error {
	_, err := g.run(ctx, dir, nil, "checkout", "--quiet", "--force", "--detach", commit)
	return err
}
