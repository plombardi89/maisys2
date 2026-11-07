package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
)

// lastSyncFormat is how status shows when maisys last synced, in local
// time.
const lastSyncFormat = "2006-01-02 15:04 MST"

func newStatusCommand(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this machine's config, system details and last sync",
		Long: `Show which machine this is, its config repo, the system maisys detects and
whether maisys supports it, when maisys last synced and how that went, and
how many things maisys manages here. status changes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showStatus(cmd, g)
		},
	}
}

// statusLines are the lines status prints, as aligned "key: value" pairs.
type statusLines [][2]string

func (l *statusLines) add(key, format string, a ...any) {
	*l = append(*l, [2]string{key, fmt.Sprintf(format, a...)})
}

// write writes the lines, with their values aligned, as are the lines of
// values that have several.
func (l *statusLines) write(w io.Writer) error {
	width := 0
	for _, line := range *l {
		width = max(width, len(line[0])+1)
	}
	indent := "\n" + strings.Repeat(" ", width+1)
	var b strings.Builder
	for _, line := range *l {
		fmt.Fprintf(&b, "%-*s %s\n", width, line[0]+":", strings.ReplaceAll(line[1], "\n", indent))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// showStatus prints the machine, its config repo, the live system, the
// last sync and how many things maisys manages. It doesn't take the lock,
// and shows what it can't find out in place of the value, rather than
// failing.
func showStatus(cmd *cobra.Command, g *globalOptions) error {
	paths, err := settings.DefaultPaths(g.goos, g.getenv)
	if err != nil {
		return err
	}
	var lines statusLines
	saved, err := settings.Load(paths.SettingsFile())
	switch {
	case errors.Is(err, settings.ErrNoSettings):
		lines.add("machine", "not set up yet: run `maisys sync CONFIG-URL` to set up this machine, or `maisys config init` to start a new config repo")
	case err != nil:
		return err
	default:
		if saved.Machine == "" {
			lines.add("machine", "not chosen yet: run `maisys sync` to choose one")
		} else {
			lines.add("machine", "%s", saved.Machine)
		}
		g.repoStatus(cmd.Context(), &lines, paths, saved)
	}

	if sys, err := g.liveSystem(); err != nil {
		lines.add("system", "unknown: %v", err)
	} else {
		lines.add("system", "%s", describeLiveSystem(sys))
		if support := sys.Support(g.now()); support.Supported {
			lines.add("support", "supported")
		} else {
			lines.add("support", "untested: %s", support.Reason)
		}
	}

	if st, err := state.Load(paths.StateFile()); err != nil {
		lines.add("last sync", "unknown: %v", err)
	} else {
		if st.LastSync.IsZero() {
			lines.add("last sync", "never")
		} else {
			lines.add("last sync", "%s (%s)", st.LastSync.Local().Format(lastSyncFormat), describeResult(st.LastSyncResult))
		}
		n := len(st.IDs())
		lines.add("managed", "%s", plural(n, "item"))
	}
	return lines.write(cmd.OutOrStdout())
}

// repoStatus adds lines about the config repo in the local settings s: its
// folder, its URL, its commit and whether it has uncommitted changes.
func (g *globalOptions) repoStatus(ctx context.Context, lines *statusLines, paths settings.Paths, s *settings.Settings) {
	if s.RepoDir == "" {
		lines.add("config repo", "not set: run `maisys sync --dir PATH` to use a clone, or `maisys sync CONFIG-URL` to clone one")
		return
	}
	if info, err := os.Stat(s.RepoDir); err != nil || !info.IsDir() {
		lines.add("config repo", "%s (it doesn't exist)", s.RepoDir)
		return
	}
	lines.add("config repo", "%s", s.RepoDir)
	if s.RepoURL == "" {
		lines.add("url", "none (the clone has no remote)")
	} else {
		lines.add("url", "%s", gitrepo.Redact(s.RepoURL))
	}

	commit, err := g.repoCommit(ctx, paths, s.RepoDir)
	if err != nil {
		lines.add("commit", "unknown: %v", err)
		return
	}
	lines.add("commit", "%s", commit)
}

// repoCommit describes the commit the clone in dir has checked out, and
// whether it has uncommitted changes.
func (g *globalOptions) repoCommit(ctx context.Context, paths settings.Paths, dir string) (string, error) {
	opts, err := g.gitOptions(paths)
	if err != nil {
		return "", err
	}
	clone, err := gitrepo.Open(ctx, dir, opts)
	if err != nil {
		return "", err
	}
	head, err := clone.Head(ctx)
	if err != nil {
		return "", err
	}
	dirty, err := clone.Dirty(ctx)
	if err != nil {
		return "", err
	}
	if head == "" {
		head = "none yet"
	}
	if dirty {
		return head + ", with uncommitted changes", nil
	}
	return head + ", no uncommitted changes", nil
}

// describeLiveSystem describes the live system for status, such as
// "Fedora Linux 44 (Workstation Edition), amd64, WSL distro fedora-dev".
func describeLiveSystem(s *facts.System) string {
	name := s.PrettyName
	if name == "" {
		name = strings.TrimSpace(s.OSID + " " + s.OSVersion)
	}
	arch := string(s.Arch)
	if arch == "" {
		arch = s.RawArch
	}
	desc := name + ", " + arch
	switch {
	case s.WSL && s.WSLDistro != "":
		desc += ", WSL distro " + s.WSLDistro
	case s.WSL:
		desc += ", in WSL"
	}
	return desc
}

// describeResult says how a sync ended.
func describeResult(r state.Result) string {
	switch r {
	case state.ResultOK:
		return "ok"
	case state.ResultFailed:
		return "some steps failed"
	case state.ResultInterrupted:
		return "interrupted"
	default:
		return string(r)
	}
}

// plural returns "1 item" or "2 items".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
