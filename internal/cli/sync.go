package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"

	"github.com/plombardi89/maisys2/internal/resource/tool"
)

// errInterrupted is how a sync stops on Ctrl-C before it changes anything.
var errInterrupted = errors.New("interrupted; nothing was changed")

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
you confirm. Outside a terminal, --yes is needed to apply changes.

The first run clones CONFIG-URL (or uses the clone given with --dir), picks the
machine file for this machine and saves both choices. Later runs need no
arguments. A different CONFIG-URL or --dir switches to that config repo, and
--machine to that machine; the change is saved.

The config repo isn't pulled with --local, or when it has uncommitted changes
or no remote.

With --dry-run, sync only shows the plan. It exits with 0 if there's nothing
to change and 2 if there is. Otherwise, the exit code is 0 if the sync worked
and 1 if anything failed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := &syncRun{g: g, cmd: cmd, opts: &opts, warned: map[string]bool{}}
			if len(args) == 1 {
				s.url = args[0]
			}
			return s.run()
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
	// cfg is the machine's merged config, once it's valid.
	cfg *config.Machine
	// warned holds the maisys.toml warnings already printed.
	warned map[string]bool
	// header holds lines for the top of the plan, saying which config repo
	// or machine this run switched to.
	header []string
	// warnings are shown in the plan, such as deprecated keys or an
	// untested system.
	warnings []string
	// planShown reports whether the plan, with header and warnings, has
	// been shown.
	planShown bool
	// pending is the local settings to save, if they change (see
	// saveSettings), and machineSource where the machine came from.
	pending       *settings.Settings
	machineSource string
}

// run runs the sync. It refuses to run as root, updates maisys, takes the
// lock, gets the config ready and checks it against the live system, then
// plans, and applies the plan once it's confirmed.
func (s *syncRun) run() error {
	sys, err := s.g.liveSystem()
	if err != nil {
		return err
	}
	if err := sys.CheckUser(); err != nil {
		return err
	}
	if !s.opts.local {
		if err := updateSelf(s.cmd, s.g); err != nil {
			return err
		}
	}

	if s.paths, err = settings.DefaultPaths(s.g.goos, s.g.getenv); err != nil {
		return err
	}
	lock, err := state.Acquire(s.paths.LockFile())
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	defer s.showUnshown()

	// --dry-run does this too, and may clone the config repo and save the
	// local settings: that's maisys's own setup, not a change to the
	// machine.
	if err := s.prepare(); err != nil {
		return err
	}
	if err := s.checkSystem(sys); err != nil {
		return err
	}
	if s.saved == nil {
		if err := s.saveSettings(); err != nil {
			return err
		}
	}
	return s.planAndApply(sys)
}

// showUnshown prints the plan's header lines and warnings if the sync
// stopped before it showed the plan, so that a switch of config repo or
// machine is still reported (it's only saved once a plan is applied).
func (s *syncRun) showUnshown() {
	if s.planShown {
		return
	}
	for _, line := range s.header {
		_, _ = fmt.Fprintf(s.cmd.OutOrStdout(), "maisys: %s\n", line)
	}
	printWarnings(s.cmd.ErrOrStderr(), s.warnings)
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
// Its warnings go into the plan.
func (s *syncRun) validate() error {
	errOut := s.cmd.ErrOrStderr()
	m, err := s.repo.Validate(s.machine)
	if err != nil {
		if m != nil {
			printWarnings(errOut, m.Warnings)
		}
		return problems(errOut, err)
	}
	s.cfg = m
	s.warnings = append(s.warnings, m.Warnings...)
	s.g.debugf(errOut, "the config for %s is valid", s.machine)
	return nil
}

// checkSystem checks the machine's config against the live system sys, and
// stops the sync before it changes anything if they don't match. If the
// rolling support policy doesn't cover sys, the plan says so.
func (s *syncRun) checkSystem(sys *facts.System) error {
	errOut := s.cmd.ErrOrStderr()
	if err := sys.CheckMachine(s.cfg, s.repo.Modules); err != nil {
		printWarnings(errOut, s.warnings)
		s.warnings = nil
		return problems(errOut, err)
	}
	if support := sys.Support(s.g.now()); !support.Supported {
		s.warnings = append([]string{"untested: " + support.Reason}, s.warnings...)
	}
	return nil
}

// planAndApply plans the sync on the live system sys and shows the plan.
// With --dry-run, that's all. Otherwise it applies the plan once it's
// confirmed, and shows what happened.
func (s *syncRun) planAndApply(sys *facts.System) error {
	ctx, out, errOut := s.cmd.Context(), s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	now := s.g.now()
	log, err := system.OpenLog(s.paths.LogsDir(), now, s.logHeader(now))
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	s.g.debugf(errOut, "the output of every command is in %s", log.Path())

	store, err := state.Open(s.paths.StateFile())
	if err != nil {
		return err
	}
	opts := system.Options{
		Log:  log,
		Sudo: system.SudoOptions{OS: s.cfg.OS, Stdin: s.g.stdin, Stdout: out, Stderr: errOut},
	}
	if s.g.verbose {
		opts.Verbose = errOut
	}
	runner := s.g.newRunner(opts)
	// Reading the caches now means one written by a newer maisys stops the
	// sync before it plans anything.
	tools := tool.NewEnv(s.paths, "maisys/"+buildinfo.Version())
	if err := tools.Load(); err != nil {
		return err
	}
	p, err := engine.NewPlan(ctx, engine.PlanInput{
		Machine:  s.cfg,
		Facts:    sys,
		Paths:    s.paths,
		Runner:   runner,
		State:    store.State,
		Modules:  s.repo.Modules,
		Removers: s.g.removers,
		Only:     s.opts.only,
		Header:   s.header,
		Warnings: s.warnings,
		Log:      log,
		Now:      s.g.now,
		Shared:   map[any]any{tool.EnvKey{}: tools},
	})
	if err != nil {
		if ctx.Err() != nil {
			return errInterrupted
		}
		return err
	}
	s.planShown = true
	p.Render(out, s.g.color && s.g.stdoutTerminal())
	p.Render(log, false)
	// Some failures, such as a failed lookup of a "latest" version, stop
	// the whole sync, even a dry run's exit code.
	if err := p.StopError(); err != nil {
		s.printLogPath(log)
		return err
	}

	if s.opts.dryRun {
		switch {
		case p.Failed():
			s.printLogPath(log)
			return exitCodeError{code: exitError}
		case p.HasChanges():
			return exitCodeError{code: exitChanges}
		}
		return nil
	}
	if p.HasChanges() {
		if ok, err := s.confirm(); err != nil || !ok {
			return err
		}
	}
	if err := s.saveSettings(); err != nil {
		return err
	}
	return s.apply(p, store, log, runner, sys)
}

// logHeader returns the top of the run's log.
func (s *syncRun) logHeader(now time.Time) string {
	what := "maisys sync"
	if s.opts.dryRun {
		what += " --dry-run"
	}
	return fmt.Sprintf("%s\nversion: %s\nmachine: %s\nconfig repo: %s\nstarted: %s\n",
		what, buildinfo.String(), s.machine, describeRepo(s.repo.URL, s.repo.dir), now.UTC().Format(time.RFC3339))
}

// confirm asks in a terminal whether to apply the plan's changes, unless
// --yes says to. Without a terminal, it needs --yes. It returns false if
// the answer is anything but yes.
func (s *syncRun) confirm() (bool, error) {
	ctx, out := s.cmd.Context(), s.cmd.OutOrStdout()
	switch {
	case s.opts.yes:
		return true, nil
	case !s.g.isTerminal():
		return false, errors.New("the plan has changes, and maisys can only ask whether to apply them in a terminal: " +
			"run maisys sync in a terminal, or with --yes to apply them without asking")
	}
	_, _ = fmt.Fprint(out, "Apply? [y/N] ")
	answer, err := s.g.readLine(ctx)
	switch {
	case ctx.Err() != nil:
		_, _ = fmt.Fprintln(out)
		return false, errInterrupted
	case err != nil && !errors.Is(err, io.EOF):
		_, _ = fmt.Fprintln(out)
		return false, err
	case err != nil:
		// Without a line ending, the answer didn't end the line.
		_, _ = fmt.Fprintln(out)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	_, err = fmt.Fprintln(out, "maisys: nothing was changed")
	return false, err
}

// apply applies the plan p, which saves the state file after every step,
// and shows what happened, including whether the live system sys
// recommends a reboot, which it asks with runner. A plan with nothing to
// change is applied too, which records the sync and forgets things that
// are already gone, but quietly, unless something failed.
func (s *syncRun) apply(p *engine.Plan, store *state.Store, log *system.Log, runner system.Runner, sys *facts.System) error {
	ctx, out := s.cmd.Context(), s.cmd.OutOrStdout()
	changes := p.HasChanges()
	progress := &countingWriter{w: out}
	in := engine.ApplyInput{Store: store, Out: out}
	if changes {
		_, _ = fmt.Fprintln(out)
		in.Progress = progress
	}
	step, stopStep := context.WithCancel(context.WithoutCancel(ctx))
	defer stopStep()
	in.Abort = step

	stopWatching := s.watchInterrupts(ctx, stopStep)
	res := engine.Apply(ctx, p, in)
	stopWatching()

	_, _ = fmt.Fprintln(log)
	res.Render(log)
	if !changes && res.OK() {
		return nil
	}
	if !changes || progress.n > 0 {
		_, _ = fmt.Fprintln(out)
	}
	res.Render(out)
	if changes && s.rebootRecommended(p, runner, sys, log) {
		_, _ = fmt.Fprintln(io.MultiWriter(out, log), rebootNotice)
	}
	if failed(res) {
		s.printLogPath(log)
	}
	if code := res.ExitCode(); code != exitOK {
		return exitCodeError{code: code}
	}
	return nil
}

// rebootNotice is how a sync's summary says a reboot is recommended.
const rebootNotice = "Reboot recommended: some upgraded packages only take effect after a restart."

// rebootRecommended reports whether the live system sys recommends a reboot
// after the plan p installed or upgraded packages, asking with runner. If
// it can't tell, it says why in the log.
func (s *syncRun) rebootRecommended(p *engine.Plan, runner system.Runner, sys *facts.System, log io.Writer) bool {
	if !changesPackages(p) {
		return false
	}
	reboot, err := pkg.RebootRecommended(s.cmd.Context(), runner, sys)
	if err != nil {
		_, _ = fmt.Fprintf(log, "couldn't tell whether a reboot is recommended: %v\n", err)
		s.g.debugf(s.cmd.ErrOrStderr(), "couldn't tell whether a reboot is recommended: %v", err)
	}
	return reboot
}

// changesPackages reports whether the plan p installs or upgrades
// packages.
func changesPackages(p *engine.Plan) bool {
	for _, m := range p.Modules {
		if slices.ContainsFunc(m.Items, func(item *engine.Item) bool {
			return item.Resource.Kind() == pkg.Kind && item.Change.Action.Applies()
		}) {
			return true
		}
	}
	return false
}

// watchInterrupts reports Ctrl-C while a plan is applied. The first, which
// cancels ctx, lets the step in progress finish. The second, which closes
// the environment's abort channel, stops that step too, with stopStep. The
// function it returns stops watching.
func (s *syncRun) watchInterrupts(ctx context.Context, stopStep context.CancelFunc) (stop func()) {
	errOut := s.cmd.ErrOrStderr()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
		case <-done:
			// A Ctrl-C during the last step stopped the sync too.
			if ctx.Err() == nil {
				return
			}
		}
		_, _ = fmt.Fprintln(errOut, "maisys: stopping after the current step… (press Ctrl-C again to stop it now)")
		select {
		case <-s.g.abort:
			_, _ = fmt.Fprintln(errOut, "maisys: stopping the current step now")
			stopStep()
		case <-done:
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

// printLogPath says where the run's log is, when something failed.
func (s *syncRun) printLogPath(log *system.Log) {
	_, _ = fmt.Fprintf(s.cmd.ErrOrStderr(), "maisys: the full output is in %s\n", log.Path())
}

// failed reports whether a step of the sync failed, as opposed to being
// skipped or interrupted.
func failed(res *engine.Result) bool {
	return res.Err != nil ||
		slices.ContainsFunc(res.Modules, func(m engine.ModuleResult) bool { return m.Err != nil }) ||
		slices.ContainsFunc(res.Removals, func(r engine.RemovalResult) bool { return r.Err != nil })
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}
