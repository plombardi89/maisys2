package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
)

// machineEnv is the environment variable that chooses the machine.
const machineEnv = "MAISYS_MACHINE"

// Where the machine name came from, as selectMachine reports it.
const (
	fromFlag     = "--machine"
	fromEnv      = machineEnv
	fromSettings = "the local settings"
	fromHostname = "the hostname"
	fromPicker   = "your choice"
)

var errNoMachineChosen = errors.New("no machine chosen")

// machineRequest says how a command selects the machine.
type machineRequest struct {
	// flag is the value of the command's --machine flag, if it has one.
	flag string
	// hasFlag reports whether the command has a --machine flag, so errors
	// can suggest it.
	hasFlag bool
	// pick lets selectMachine ask which machine this is, in a terminal,
	// when nothing else names one.
	pick bool
}

// selectMachine returns the name of this machine's file in r, and where the
// name came from. It checks, in order: the --machine flag, MAISYS_MACHINE,
// machine in the local settings, and the hostname, in lower case. A name
// from one of the first three must have a machine file; the hostname is
// skipped if it doesn't. If none of them names a machine and req.pick is
// set, it asks in a terminal which of the machines for this system this
// is. Otherwise it fails, listing those machines.
func (g *globalOptions) selectMachine(cmd *cobra.Command, r *configRepo, req machineRequest) (name, source string, err error) {
	var saved string
	if r.settings != nil {
		saved = r.settings.Machine
	}
	for _, c := range []struct{ name, source string }{
		{req.flag, fromFlag},
		{g.getenv(machineEnv), fromEnv},
		{saved, fromSettings},
	} {
		if c.name == "" {
			continue
		}
		if r.hasMachine(c.name) {
			return c.name, c.source, nil
		}
		msg := fmt.Sprintf("%s doesn't exist in %s (the machine name %q comes from %s)", config.MachineFile(c.name), r.dir, c.name, c.source)
		found, err := g.machinesForSystem(r)
		if err != nil {
			return "", "", fmt.Errorf("%s, and %w", msg, err)
		}
		return "", "", fmt.Errorf("%s\n%s", msg, found)
	}

	sys, err := g.liveSystem()
	if err != nil {
		return "", "", err
	}
	host := strings.ToLower(sys.Hostname)
	names, err := r.MachineNames()
	if err != nil {
		return "", "", err
	}
	if slices.Contains(names, host) {
		return host, fromHostname, nil
	}

	found, err := g.machinesForSystem(r)
	if err != nil {
		return "", "", err
	}
	if req.pick && g.isTerminal() {
		name, err := g.pickMachine(cmd, found)
		return name, fromPicker, err
	}
	how := "set " + machineEnv + " to choose one"
	if req.hasFlag {
		how = "choose one with --machine or " + machineEnv
	}
	return "", "", fmt.Errorf("can't tell which machine this is: no machine file in %s is named after the hostname %q\n%s\n%s", r.dir, host, found, how)
}

// hasMachine reports whether the repo has a machine file for name.
func (r *configRepo) hasMachine(name string) bool {
	info, err := fs.Stat(r.FS, config.MachineFile(name))
	return err == nil && info.Mode().IsRegular()
}

// systemMachines are the machines in a config repo for the live system.
type systemMachines struct {
	// dir is the config repo's folder.
	dir    string
	system *facts.System
	// matching are the machines whose os is the system's, and whose arch,
	// if they set one, is too.
	matching []string
	// broken are the machine files that don't load, so their os isn't
	// known.
	broken []string
}

// machinesForSystem finds the machines in r for the live system.
func (g *globalOptions) machinesForSystem(r *configRepo) (*systemMachines, error) {
	sys, err := g.liveSystem()
	if err != nil {
		return nil, err
	}
	names, err := r.MachineNames()
	if err != nil {
		return nil, err
	}
	found := &systemMachines{dir: r.dir, system: sys}
	for _, name := range names {
		m, err := r.LoadMachine(name)
		switch {
		case err != nil:
			found.broken = append(found.broken, name)
		case sys.OS != "" && m.OS == sys.OS && (m.Arch == "" || m.Arch == sys.Arch):
			found.matching = append(found.matching, name)
		default:
		}
	}
	return found, nil
}

// String lists the machines, on one line, and the machine files that
// don't load, on another.
func (s *systemMachines) String() string {
	var b strings.Builder
	if len(s.matching) == 0 {
		b.WriteString(s.noneMatch())
	} else {
		_, _ = fmt.Fprintf(&b, "machines for this system (%s): %s", describeSystem(s.system), strings.Join(s.matching, ", "))
	}
	if len(s.broken) > 0 {
		b.WriteString("\n" + s.brokenNote())
	}
	return b.String()
}

func (s *systemMachines) noneMatch() string {
	return fmt.Sprintf("no machine file in %s is for this system (%s)", s.dir, describeSystem(s.system))
}

func (s *systemMachines) brokenNote() string {
	return fmt.Sprintf("skipped machine files that don't load: %s (run `maisys config validate --all` to see why)", strings.Join(s.broken, ", "))
}

// describeSystem describes s the way machine files do, such as
// "fedora 44, amd64".
func describeSystem(s *facts.System) string {
	desc := string(s.OS)
	if desc == "" {
		desc = s.OSID
	}
	if s.OSVersion != "" {
		desc += " " + s.OSVersion
	}
	arch := string(s.Arch)
	if arch == "" {
		arch = s.RawArch
	}
	return desc + ", " + arch
}

// pickMachine asks which of the machines for this system this is, until
// the answer is one of their numbers (or names), or nothing, which
// cancels.
func (g *globalOptions) pickMachine(cmd *cobra.Command, found *systemMachines) (string, error) {
	if len(found.matching) == 0 {
		msg := found.noneMatch() + "; `maisys config init` can create one"
		if len(found.broken) > 0 {
			msg += "\n" + found.brokenNote()
		}
		return "", errors.New(msg)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "maisys: which machine is this? The machines for this system (%s) are:\n", describeSystem(found.system))
	for i, name := range found.matching {
		_, _ = fmt.Fprintf(out, "  %d) %s\n", i+1, name)
	}
	if len(found.broken) > 0 {
		_, _ = fmt.Fprintf(out, "maisys: %s\n", found.brokenNote())
	}
	for {
		_, _ = fmt.Fprintf(out, "Enter a number from 1 to %d, or nothing to cancel: ", len(found.matching))
		answer, err := g.readLine(cmd.Context())
		answer = strings.TrimSpace(answer)
		switch {
		case err != nil && !errors.Is(err, io.EOF):
			_, _ = fmt.Fprintln(out)
			return "", fmt.Errorf("%w: %w", errNoMachineChosen, err)
		case answer == "":
			if err != nil {
				// Without a line ending, the answer didn't end the line.
				_, _ = fmt.Fprintln(out)
			}
			return "", errNoMachineChosen
		}
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(found.matching) {
			return found.matching[n-1], nil
		}
		if slices.Contains(found.matching, answer) {
			return answer, nil
		}
		if err != nil {
			// stdin has ended, so asking again won't get an answer.
			_, _ = fmt.Fprintln(out)
			return "", fmt.Errorf("%w: %q isn't one of the numbers", errNoMachineChosen, answer)
		}
		_, _ = fmt.Fprintf(out, "maisys: %q isn't one of the numbers above\n", answer)
	}
}

// readLine reads a line from stdin, without its line ending. It stops
// waiting if ctx is cancelled, as Ctrl-C does; maisys then exits, leaving
// the read unfinished.
func (g *globalOptions) readLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := g.in.ReadString('\n')
		done <- result{strings.TrimRight(line, "\r\n"), err}
	}()
	select {
	case res := <-done:
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
