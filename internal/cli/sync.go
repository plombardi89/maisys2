package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
)

type syncOptions struct {
	machine string
	dir     string
	dryRun  bool
	yes     bool
	only    []string
	local   bool
}

func newSyncCommand(g *globalOptions) *cobra.Command {
	var opts syncOptions
	cmd := &cobra.Command{
		Use:   "sync [CONFIG-URL]",
		Short: "Bring this machine in line with its config",
		Long: `Update maisys, pull the config repo, show what would change, and apply it after
you confirm.

The first run clones CONFIG-URL (or uses the clone given with --dir), picks the
machine file for this machine and saves both choices. Later runs need no
arguments. A different CONFIG-URL or --dir switches to that config repo, and
--machine to that machine; the change is saved.

The config repo isn't pulled with --local, or when it has uncommitted changes
or no remote.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.local {
				if err := updateSelf(cmd, g); err != nil {
					return err
				}
			}
			s := &syncRun{g: g, cmd: cmd, opts: &opts, warned: map[string]bool{}}
			if len(args) == 1 {
				s.url = args[0]
			}
			// --dry-run does this too, and may clone the config repo and
			// save the local settings: that's maisys's own setup, not a
			// change to the machine.
			if err := s.prepare(); err != nil {
				return err
			}
			return notImplemented(cmd, args)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.machine, "machine", "", "machine file to use; the choice is saved for later runs")
	f.StringVar(&opts.dir, "dir", "", "where the config repo is cloned (default ~/maisys-config)")
	f.BoolVarP(&opts.dryRun, "dry-run", "n", false, "show the plan without changing anything; exits 2 if there are changes")
	f.BoolVarP(&opts.yes, "yes", "y", false, "apply without asking for confirmation")
	f.StringSliceVar(&opts.only, "only", nil, "limit the sync to these modules")
	f.BoolVar(&opts.local, "local", false, "don't update maisys or pull the config repo")
	return cmd
}

// syncRun is one run of sync, and what it has found out so far.
type syncRun struct {
	g    *globalOptions
	cmd  *cobra.Command
	opts *syncOptions
	// url is CONFIG-URL, if it was given.
	url   string
	paths settings.Paths
	// saved are the local settings from before this run, or nil on the
	// first run.
	saved *settings.Settings
	// repo is the config repo this run uses.
	repo *configRepo
	// clone is the config repo's clone, once git has opened it.
	clone *gitrepo.Repo
	// cloned reports whether this run cloned the config repo, so there's
	// nothing to pull.
	cloned bool
	// switched reports whether this run switched to another config repo.
	switched bool
	// machine is the name of this machine's file.
	machine string
	// warned holds the maisys.toml warnings already printed.
	warned map[string]bool
}

// prepare gets the config ready to plan with. It sets maisys up on the
// first run, switches config repo or machine when asked to, pulls the
// config repo, and validates this machine's config.
func (s *syncRun) prepare() error {
	if err := s.findRepo(); err != nil {
		return err
	}
	switch {
	case s.opts.local:
		s.g.debugf(s.cmd.ErrOrStderr(), "not pulling the config repo: --local")
	case s.cloned:
	default:
		if err := s.pull(); err != nil {
			return err
		}
	}
	if err := s.repo.checkMarker(s.cmd.ErrOrStderr(), s.warned); err != nil {
		return notConfigRepo(err, s.repo.dir)
	}
	if err := s.chooseMachine(); err != nil {
		return err
	}
	return s.validate()
}

// pull fast-forwards the config repo to its remote, unless it has no
// remote or has uncommitted changes.
func (s *syncRun) pull() error {
	ctx, errOut, dir := s.cmd.Context(), s.cmd.ErrOrStderr(), s.repo.dir
	if s.clone == nil {
		opts, err := s.g.gitOptions(s.paths)
		if err != nil {
			return err
		}
		if s.clone, err = gitrepo.Open(ctx, dir, opts); err != nil {
			return err
		}
	}
	url, err := s.clone.RemoteURL(ctx)
	if err != nil {
		return err
	}
	if url == "" {
		s.g.debugf(errOut, "not pulling the config repo in %s: it has no remote", dir)
		return nil
	}
	dirty, err := s.clone.Dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		_, _ = fmt.Fprintf(errOut, "maisys: warning: the config repo in %s has uncommitted changes: using the config as it is, without pulling\n", dir)
		return nil
	}
	s.g.debugf(errOut, "pulling the config repo in %s from %s", dir, gitrepo.Redact(url))
	return s.clone.Pull(ctx)
}

// validate checks this machine's config, and prints the problems it finds.
func (s *syncRun) validate() error {
	errOut := s.cmd.ErrOrStderr()
	if _, err := s.repo.Validate(s.machine); err != nil {
		return problems(errOut, err)
	}
	s.g.debugf(errOut, "the config for %s is valid", s.machine)
	return nil
}
