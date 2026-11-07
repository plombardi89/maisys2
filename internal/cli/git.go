package cli

import (
	"errors"
	"fmt"

	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
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
