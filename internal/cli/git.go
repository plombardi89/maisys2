package cli

import (
	"errors"
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/system"
)

// configTokenEnv is used as the HTTPS password for the config repo, and is
// never written to disk.
const configTokenEnv = "MAISYS_CONFIG_TOKEN"

// gitOptions returns what's needed to run git on the config repo: the git
// binary, and how to sign in to the repo.
func (g *globalOptions) gitOptions(paths settings.Paths) (gitrepo.Options, error) {
	if g.findGit == nil {
		return gitrepo.Options{}, errors.New("can't run git")
	}
	git, err := g.findGit()
	if err != nil {
		return gitrepo.Options{}, fmt.Errorf("%w\n%s", err, installGitHint(g.goos))
	}
	opts := gitrepo.Options{
		Git: git,
		Env: g.gitEnv,
		Auth: gitrepo.Auth{
			Token:           g.getenv(configTokenEnv),
			CredentialsFile: paths.CredentialsFile(),
			Prompt:          g.prompt,
			// The prompt asks on stdout (see Execute).
			PromptOut: g.stdout,
		},
	}
	// Like every command's output, git's is only streamed with --verbose.
	if g.verbose {
		opts.Progress = g.stderr
	}
	return opts, nil
}

// installGitHint says how to install git, which maisys needs for the config
// repo.
func installGitHint(goos string) string {
	if goos == "windows" {
		return "Install it with: winget install --id Git.Git -e"
	}
	return "Install it with: sudo dnf install git-core (Fedora) or sudo apt-get install git (Ubuntu)"
}

// gitPackages are the packages that install git on each Linux OS.
var gitPackages = map[config.OS]string{config.Fedora: "git-core", config.Ubuntu: "git"}

// installGit installs git if it's missing, as the first step of setting up
// a Fedora or Ubuntu machine. That needs root: it checks that sudo works,
// then starts sudo's session, which may ask for your password. Elsewhere,
// and if git is there, it does nothing: a missing git is reported where
// it's needed, with how to install it.
func (s *syncRun) installGit() error {
	if s.g.findGit == nil {
		return nil
	}
	if _, err := s.g.findGit(); !errors.Is(err, gitrepo.ErrNoGit) {
		return nil
	}
	sys, err := s.g.liveSystem()
	if err != nil {
		return err
	}
	name, ok := gitPackages[sys.OS]
	if !ok {
		return nil
	}
	ctx, out, errOut := s.cmd.Context(), s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	opts := system.Options{Sudo: system.SudoOptions{OS: sys.OS, Stdin: s.g.stdin, Stdout: out, Stderr: errOut}}
	if s.g.verbose {
		opts.Verbose = errOut
	}
	runner := s.g.newRunner(opts)
	m, err := pkg.NewManager(runner, sys)
	if err != nil {
		return err
	}
	m.Notify = errOut

	_, _ = fmt.Fprintf(out, "maisys: installing git (%s), which maisys needs for the config repo\n", name)
	if sudo, ok := runner.(system.SudoRunner); ok {
		if _, err := sudo.CheckSudo(ctx); err != nil {
			return fmt.Errorf("installing git: %w", err)
		}
		if err := sudo.StartSudo(ctx); err != nil {
			return fmt.Errorf("installing git: %w", err)
		}
		defer sudo.StopSudo()
	}
	if err := m.Install(ctx, name); err != nil {
		return fmt.Errorf("installing git: %w", err)
	}
	return nil
}
