package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
	"github.com/plombardi89/maisys2/internal/settings"
)

// initCommitMessage is the message of a new config repo's first commit.
const initCommitMessage = "Start a maisys config"

// initOptions are the argument and flags of config init.
type initOptions struct {
	dir     string
	machine string
	remote  string
}

// initConfig creates a starter config repo with a machine file for this
// machine, commits it, and saves it in the local settings as this
// machine's config. With a remote, it pushes the repo there too.
// Everything that can fail is checked before anything is written.
func initConfig(cmd *cobra.Command, g *globalOptions, opts initOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	paths, err := settings.DefaultPaths(g.goos, g.getenv)
	if err != nil {
		return err
	}
	dir, err := g.initDir(opts.dir, paths)
	if err != nil {
		return err
	}
	if err := checkEmpty(dir); err != nil {
		return err
	}
	sys, err := g.liveSystem()
	if err != nil {
		return err
	}
	if !slices.Contains(config.AllOS(), sys.OS) {
		return fmt.Errorf("can't create a config for this system (%s): maisys supports Fedora, Ubuntu and Windows", describeSystem(sys))
	}
	name, err := g.initMachineName(opts.machine, sys)
	if err != nil {
		return err
	}
	gitOpts, err := g.gitOptions(paths)
	if err != nil {
		return err
	}
	if err := gitrepo.CheckIdentity(ctx, gitOpts); err != nil {
		return err
	}
	settingsFile := paths.SettingsFile()
	old, err := settings.Load(settingsFile)
	if err != nil && !errors.Is(err, settings.ErrNoSettings) {
		return err
	}

	s := &config.Scaffold{
		Machine:    name,
		OS:         sys.OS,
		OSVersion:  sys.OSVersion,
		Modules:    modules.All(),
		Docs:       modules.Docs(),
		UpdateRepo: buildinfo.UpdateRepo(),
	}
	if v := buildVersion(); v != buildinfo.DevVersion {
		s.Version = v
	}
	if sys.OS == config.Windows {
		s.ConfigURL = wslhost.HTTPSURL(opts.remote)
		if s.DistroUser, err = g.askDistroUser(cmd); err != nil {
			return err
		}
	}
	files, err := s.Files()
	if err != nil {
		return err
	}
	if err := checkScaffold(files, name, opts.remote); err != nil {
		return err
	}

	if err := writeScaffold(dir, files); err != nil {
		return err
	}
	desc := fmt.Sprintf("%s (%s)", name, sys.OS)
	if sys.OS == config.Windows {
		desc = fmt.Sprintf("%s (windows), and %s for its WSL distro %s (user %s)", name, s.DistroMachine(), config.ScaffoldDistro, s.DistroUser)
	}
	_, _ = fmt.Fprintf(out, "maisys: created a config repo in %s for %s:\n", dir, desc)
	for _, file := range slices.Sorted(maps.Keys(files)) {
		_, _ = fmt.Fprintf(out, "  %s\n", file)
	}

	repo, err := gitrepo.Init(ctx, dir, gitOpts)
	if err == nil {
		err = repo.CommitAll(ctx, initCommitMessage)
	}
	if err != nil {
		return fmt.Errorf("%w\nThe files are in %s, but aren't committed", err, dir)
	}
	_, _ = fmt.Fprintf(out, "maisys: committed them with git (%q)\n", initCommitMessage)

	saved := &settings.Settings{RepoDir: dir, RepoURL: opts.remote, Machine: name}
	if err := saved.Save(settingsFile); err != nil {
		return fmt.Errorf("%w\nThe config repo is committed in %s", err, dir)
	}
	reportSettings(out, settingsFile, old, saved)

	var pushErr error
	if opts.remote != "" {
		pushErr = pushScaffold(cmd, repo, opts.remote)
		if pushErr == nil {
			_, _ = fmt.Fprintln(out, "maisys: pushed it to origin")
		}
	}
	printNextSteps(out, dir, s, opts.remote != "")
	return pushErr
}

// initDir returns the absolute path of the folder config init writes to:
// dir, with a leading "~" for the home folder and relative to the working
// directory, or else the default.
func (g *globalOptions) initDir(dir string, paths settings.Paths) (string, error) {
	if dir == "" {
		return filepath.Clean(paths.DefaultRepoDir()), nil
	}
	return g.absPath(dir, paths.Home)
}

// checkEmpty checks that dir is an empty folder, or doesn't exist.
func checkEmpty(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s already exists, and isn't a folder: choose a new or empty folder for the config repo", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s already exists, and isn't empty: choose a new or empty folder for the config repo", dir)
	}
	return nil
}

// initMachineName returns the name of the machine file for this machine:
// the --machine flag, or else MAISYS_MACHINE, or else the hostname in lower
// case.
func (g *globalOptions) initMachineName(flag string, sys *facts.System) (string, error) {
	for _, c := range []struct{ name, source string }{
		{flag, fromFlag},
		{g.getenv(machineEnv), fromEnv},
	} {
		if c.name == "" {
			continue
		}
		if !config.ValidName(c.name) {
			return "", fmt.Errorf("invalid machine name %q (from %s): %s, starting with a letter or digit", c.name, c.source, config.NameRule)
		}
		return c.name, nil
	}
	name := strings.ToLower(sys.Hostname)
	if !config.ValidName(name) {
		return "", fmt.Errorf("the hostname %q isn't a valid machine name (%s, starting with a letter or digit): choose a name with --machine", sys.Hostname, config.NameRule)
	}
	return name, nil
}

// askDistroUser returns the Linux user a Windows host's distro creates. In
// a terminal, it asks, suggesting the Windows user name in lower case if
// that's a valid Linux user name. Elsewhere, it uses that name.
func (g *globalOptions) askDistroUser(cmd *cobra.Command) (string, error) {
	windowsUser := g.getenv("USERNAME")
	suggested := strings.ToLower(windowsUser)
	if !wslhost.ValidUser(suggested) {
		suggested = ""
	}
	if !g.isTerminal() {
		if suggested == "" {
			return "", fmt.Errorf("can't choose the Linux user for the WSL distro %s: the Windows user name %q isn't a valid Linux user name, so run maisys config init in a terminal to choose one", config.ScaffoldDistro, windowsUser)
		}
		return suggested, nil
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "maisys: the config creates a WSL distro, %s, with a Linux user for you.\n", config.ScaffoldDistro)
	for {
		if suggested != "" {
			_, _ = fmt.Fprintf(out, "Linux user name [%s]: ", suggested)
		} else {
			_, _ = fmt.Fprint(out, "Linux user name: ")
		}
		answer, err := g.readLine(cmd.Context())
		answer = strings.TrimSpace(answer)
		if err != nil {
			// Without a line ending, the answer didn't end the line.
			_, _ = fmt.Fprintln(out)
			if !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("no Linux user chosen: %w", err)
			}
		}
		switch {
		case answer == "" && suggested != "":
			return suggested, nil
		case wslhost.ValidUser(answer):
			return answer, nil
		case err != nil:
			// stdin has ended, so asking again won't get an answer.
			if answer == "" {
				return "", errors.New("no Linux user chosen")
			}
			return "", fmt.Errorf("no Linux user chosen: %q isn't a valid Linux user name", answer)
		case answer == "":
		default:
			_, _ = fmt.Fprintf(out, "maisys: %q isn't a valid Linux user name: use one %s, other than root\n", answer, wslhost.UserRule)
		}
	}
}

// checkScaffold validates the starter config before it's written: this
// machine's config, as a repo cloned from url, then every machine's.
func checkScaffold(files map[string]string, machine, url string) error {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(data), Mode: 0o644}
	}
	r := &config.Repo{FS: fsys, Modules: modules.All(), URL: url, Strict: true}
	_, err := r.Validate(machine)
	if err == nil {
		_, err = r.ValidateAll()
	}
	if err != nil {
		return fmt.Errorf("the starter config doesn't pass validation, so it wasn't written:\n%w", err)
	}
	return nil
}

// writeScaffold writes files, keyed by their slash-separated paths, into
// dir.
func writeScaffold(dir string, files map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// reportSettings says what changed in the local settings, saved in file,
// from old (nil if there were none) to saved.
func reportSettings(out io.Writer, file string, old, saved *settings.Settings) {
	if old == nil {
		_, _ = fmt.Fprintf(out, "maisys: saved it as this machine's config in %s\n", file)
		return
	}
	_, _ = fmt.Fprintf(out, "maisys: replaced the local settings in %s\n", file)
	if old.RepoDir != "" && !sameDir(old.RepoDir, saved.RepoDir) {
		_, _ = fmt.Fprintf(out, "maisys: this machine now uses %s (was %s)\n", saved.RepoDir, old.RepoDir)
	}
	if old.Machine != "" && old.Machine != saved.Machine {
		_, _ = fmt.Fprintf(out, "maisys: this machine is now %s (was %s)\n", saved.Machine, old.Machine)
	}
}

// pushScaffold sets url as the repo's origin and pushes to it.
func pushScaffold(cmd *cobra.Command, repo *gitrepo.Repo, url string) error {
	err := repo.SetRemote(cmd.Context(), url)
	if err == nil {
		err = repo.Push(cmd.Context())
	}
	if err != nil {
		return fmt.Errorf("%w\nThe files are committed in %s. To push them later, run:\n  git -C %s push -u origin HEAD", err, repo.Dir, quoteArg(repo.Dir))
	}
	return nil
}

// printNextSteps says what to do with a new config repo in dir, which has a
// remote if hasRemote is set.
func printNextSteps(out io.Writer, dir string, s *config.Scaffold, hasRemote bool) {
	edit := fmt.Sprintf("in profiles/base.toml,\n    and list the packages you want in profiles/%s.toml", s.OS)
	if s.OS == config.Windows {
		edit = fmt.Sprintf("in profiles/windows.toml,\n    and for the WSL distro %s, in profiles/base.toml and profiles/ubuntu.toml", config.ScaffoldDistro)
	}
	commit := "Commit your changes with git, and push them."
	if !hasRemote {
		commit = "Commit your changes with git."
	}
	_, _ = fmt.Fprintf(out, `
Next:
  - Edit the files: turn on the modules you want %s.
  - Check the config: maisys config validate
  - See what a sync would change: maisys sync --dry-run
  - %s
`, edit, commit)
	if hasRemote {
		return
	}
	other := "other machines"
	if s.OS == config.Windows {
		other += ", including the WSL distro " + config.ScaffoldDistro + ","
	}
	_, _ = fmt.Fprintf(out, `
To sync %s from this config, it needs a hosted git repo. Create an
empty one, then run:
  git -C %s remote add origin URL
  git -C %s push -u origin HEAD
`, other, quoteArg(dir), quoteArg(dir))
}

// quoteArg quotes s, if it needs quotes, for a command people copy into a
// shell.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'`$&|;<>()*?[]{}!#") {
		return s
	}
	return `"` + s + `"`
}
