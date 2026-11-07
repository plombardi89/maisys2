package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/system"
)

// appInstaller is the package family of App Installer, which has winget.
const appInstaller = "Microsoft.DesktopAppInstaller_8wekyb3d8bbwe"

// registerWingetCmd registers App Installer, and with it winget, for the
// user. A fresh Windows install comes with it, but may only register it
// once the Store has updated it, which can take a while.
var registerWingetCmd = system.Cmd{
	Name: "powershell.exe",
	Args: []string{
		"-NoProfile", "-NonInteractive", "-Command",
		"Add-AppxPackage -RegisterByFamilyName -MainPackage " + appInstaller,
	},
	Label: "registering winget",
}

// detectWindows detects the details of the Windows host sys, with runner,
// unless they've been detected already. What it couldn't detect goes to
// log, if it isn't nil, and with --verbose to stderr.
func (s *syncRun) detectWindows(sys *facts.System, runner system.Runner, log io.Writer) error {
	if sys.OS != config.Windows || sys.Windows != nil {
		return nil
	}
	w, err := s.g.detectWindows(s.cmd.Context(), runner)
	if err != nil {
		return fmt.Errorf("detecting this Windows host's details: %w", err)
	}
	sys.Windows = w
	for _, warning := range w.Warnings {
		s.g.debugf(s.cmd.ErrOrStderr(), "%s", warning)
		if log != nil {
			_, _ = fmt.Fprintln(log, warning)
		}
	}
	return nil
}

// preflight runs the checks of a Windows host's first sync before it
// changes anything: Windows 11, virtualization turned on, winget, and a
// local admin (see facts.System.CheckWindowsHost). If winget is missing,
// it registers it first, as a fresh install may need. It does nothing
// elsewhere.
func (s *syncRun) preflight() error {
	sys, err := s.g.liveSystem()
	if err != nil {
		return err
	}
	if sys.OS != config.Windows {
		return nil
	}
	ctx, out, errOut := s.cmd.Context(), s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	var opts system.Options
	if s.g.verbose {
		opts.Verbose = errOut
	}
	runner := s.g.newRunner(opts)
	if err := s.detectWindows(sys, runner, nil); err != nil {
		return err
	}

	w := sys.Windows
	if w.Winget == "" {
		_, _ = fmt.Fprintln(out, "maisys: registering winget, which a fresh Windows install may not have done yet")
		if _, err := runner.Run(ctx, registerWingetCmd); err != nil {
			// The check below says what to do.
			s.g.debugf(errOut, "%v", err)
		} else if w.Winget, err = facts.DetectWinget(ctx, runner); err != nil {
			s.g.debugf(errOut, "%v", err)
		}
		if ctx.Err() != nil {
			return errInterrupted
		}
	}
	if w.Virtualization == facts.VirtualizationUnknown {
		_, _ = fmt.Fprintln(errOut, "maisys: warning: couldn't tell whether virtualization is turned on in this PC's firmware, which WSL 2 needs; carrying on")
	}
	if err := sys.CheckWindowsHost(); err != nil {
		_, _ = fmt.Fprintln(errOut, "maisys: this Windows host isn't ready for maisys yet:")
		return problems(errOut, err)
	}
	return nil
}

// newHelperCommand returns the hidden command that runs the elevated helper
// on Windows. maisys starts it elevated while it applies a sync, and it
// runs the commands that need admin rights, which maisys sends it over its
// pipe.
func newHelperCommand(g *globalOptions) *cobra.Command {
	var pipe, secret string
	cmd := &cobra.Command{
		Use:    system.HelperCommand,
		Short:  "Run the commands of a sync that need admin rights (Windows; maisys starts it)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return g.serveHelper(cmd.Context(), pipe, secret)
		},
	}
	cmd.Flags().StringVar(&pipe, "pipe", "", "the pipe maisys made for the helper")
	cmd.Flags().StringVar(&secret, "secret", "", "the secret the helper says hello with")
	_ = cmd.MarkFlagRequired("pipe")
	_ = cmd.MarkFlagRequired("secret")
	return cmd
}
