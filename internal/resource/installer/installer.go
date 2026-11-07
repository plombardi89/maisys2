// Package installer installs things with their vendor's installer script,
// such as SDKMAN's and opencode's, which modules configure.
//
// The script is downloaded over HTTPS, checked against a pinned sha256 if
// the module has one, and run with a shell. Whether the install is complete
// is up to the module: each has its own check, such as SDKMAN's
// var/version_native existing and sdk loading. A run that fails, or that
// finishes without a complete install, is retried with backoff, deleting
// what a half-finished install leaves behind before each attempt; if no
// attempt works, those leftovers are deleted again, and the error has the
// last attempt's output.
//
// Installs aren't removed when they're taken out of the config: the
// removal rules leave them alone. The plan says they're no longer used,
// and their records stay.
package installer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource an Installer is.
const Kind = "installer"

// DefaultAttempts is how many times an Installer runs its script before
// giving up, unless its Attempts says otherwise.
const DefaultAttempts = 3

// maxScriptSize is the largest script an Installer downloads.
const maxScriptSize = 10 << 20

// defaultClient downloads scripts unless an Installer has its own client.
var defaultClient = &http.Client{Timeout: 2 * time.Minute}

// Status is what a module's check of its install found.
type Status struct {
	// Complete reports whether the install is complete and works.
	Complete bool
	// Reason, if the install isn't complete, says why in a few words, such
	// as "version 1.4.2 is older than 2". The plan shows it.
	Reason string
}

// Installer installs something by running its vendor's installer script.
// Its ID is "installer:" followed by its name.
type Installer struct {
	// Name names the install in the plan and the state file, such as
	// "sdkman".
	Name string
	// URL is where the script is downloaded from. It must be an HTTPS URL.
	URL string
	// SHA256, if set, is the script's sha256, in hex. A script with
	// another sha256 isn't run.
	SHA256 string
	// Shell runs the script, as Shell <script> Args... Empty means bash.
	Shell string
	// Args are the script's arguments.
	Args []string
	// Env is extra environment for the script, as NAME=value.
	Env []string
	// Complete checks the install. It only reads, like a resource's Check,
	// and runs before and after each attempt. It's required.
	Complete func(ctx context.Context, rt *resource.Runtime) (Status, error)
	// Leftovers are the absolute paths a half-finished install leaves
	// behind, such as ~/.sdkman. If one exists while the install isn't
	// complete, the plan calls the install a repair. They're deleted
	// before each attempt, and after the last one if it fails.
	Leftovers []string
	// Paths are the paths the install owns, for the ownership check and the
	// state file.
	Paths []string
	// Attempts is how many times to run the script before giving up. Zero
	// means DefaultAttempts.
	Attempts int
	// Backoff returns how long to wait before the given attempt (2, 3 and
	// so on). Nil means DefaultBackoff.
	Backoff func(attempt int) time.Duration
	// Sleep waits for d, or until ctx is done. Nil means it really waits;
	// tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Client downloads the script. Nil means an http.Client with a
	// timeout.
	Client *http.Client
}

var _ resource.Resource = (*Installer)(nil)

// DefaultBackoff waits 5 seconds before the second attempt, and twice as
// long before each one after that, up to a minute.
func DefaultBackoff(attempt int) time.Duration {
	d := 5 * time.Second
	for range attempt - 2 {
		if d *= 2; d >= time.Minute {
			return time.Minute
		}
	}
	return d
}

// ID returns "installer:" followed by the install's name.
func (in *Installer) ID() string { return Kind + ":" + in.Name }

// Kind returns "installer".
func (in *Installer) Kind() string { return Kind }

// Targets returns the paths the install owns.
func (in *Installer) Targets() []string { return in.Paths }

// Privileged returns false: installer scripts run as the user.
func (in *Installer) Privileged() bool { return false }

// Check runs the module's check of the install. An install that isn't
// complete is a repair if something it leaves behind exists, and the plan
// notes that it's deleted first.
func (in *Installer) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := in.validate(); err != nil {
		return resource.Change{}, err
	}
	st, err := in.Complete(ctx, rt)
	if err != nil || st.Complete {
		return resource.Change{}, err
	}
	left, err := in.leftovers()
	if err != nil {
		return resource.Change{}, err
	}
	if len(left) == 0 {
		ch := resource.Change{Action: resource.Create, Summary: "install " + in.Name}
		if st.Reason != "" {
			ch.Summary += " (" + st.Reason + ")"
		}
		return ch, nil
	}
	ch := resource.Change{Action: resource.Update, Summary: fmt.Sprintf("repair %s (%s)", in.Name, cmp.Or(st.Reason, "half-finished install"))}
	for _, p := range left {
		ch.Notes = append(ch.Notes, tilde(rt.Paths.Home, p)+" will be deleted first")
	}
	return ch, nil
}

// validate checks the installer's fields.
func (in *Installer) validate() error {
	if in.Name == "" {
		return errors.New("an installer has no name")
	}
	if u, err := url.Parse(in.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s: the installer's URL %q isn't an HTTPS URL", in.Name, in.URL)
	}
	if in.SHA256 != "" {
		if b, err := hex.DecodeString(in.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("%s: %q isn't a sha256", in.Name, in.SHA256)
		}
	}
	if in.Complete == nil {
		return fmt.Errorf("%s: the installer has no completeness check", in.Name)
	}
	for _, p := range in.Leftovers {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s: %q isn't an absolute path", in.Name, p)
		}
	}
	return nil
}

// leftovers returns the paths a half-finished install leaves behind that
// exist.
func (in *Installer) leftovers() ([]string, error) {
	var left []string
	for _, p := range in.Leftovers {
		if _, err := os.Lstat(p); err == nil {
			left = append(left, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return left, nil
}

// removeLeftovers deletes what a half-finished install leaves behind.
func (in *Installer) removeLeftovers() error {
	var errs []error
	for _, p := range in.Leftovers {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Errorf("couldn't delete what a half-finished install of %s left: %w", in.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Apply downloads the script, and runs it until the install is complete or
// it runs out of attempts.
func (in *Installer) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := in.validate(); err != nil {
		return state.Record{}, err
	}
	attempts := max(1, cmp.Or(in.Attempts, DefaultAttempts))
	backoff := in.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	var s script
	defer s.remove()

	var err error
	n := 1
	for ; ; n++ {
		if err = in.attempt(ctx, rt, &s); err == nil {
			return state.Record{
				Paths:   in.Paths,
				Details: map[string]string{"name": in.Name, "url": in.URL, "sha256": s.sum},
			}, nil
		}
		if n == attempts || errors.Is(err, errChecksum) || ctx.Err() != nil {
			break
		}
		wait := backoff(n + 1)
		in.log(rt, "attempt %d of %d failed, trying again in %s: %v", n, attempts, wait, err)
		if serr := in.sleep(ctx, wait); serr != nil {
			break
		}
	}
	if rerr := in.removeLeftovers(); rerr != nil {
		err = errors.Join(err, rerr)
	}
	if n == 1 {
		return state.Record{}, fmt.Errorf("installing %s failed: %w", in.Name, err)
	}
	return state.Record{}, fmt.Errorf("installing %s failed after %d attempts: %w", in.Name, n, err)
}

// errChecksum means the script isn't the one the module pinned, which
// trying again won't fix.
var errChecksum = errors.New("the installer script's sha256 is wrong")

// attempt runs the script once, after downloading it into s if it isn't
// yet, and checks the install.
func (in *Installer) attempt(ctx context.Context, rt *resource.Runtime, s *script) error {
	if s.path == "" {
		if err := in.download(ctx, rt, s); err != nil {
			return err
		}
	}
	if err := in.removeLeftovers(); err != nil {
		return err
	}
	res, err := rt.Runner.Run(ctx, system.Cmd{
		Name:  cmp.Or(in.Shell, "bash"),
		Args:  append([]string{s.path}, in.Args...),
		Env:   in.Env,
		Label: "the " + in.Name + " installer",
	})
	if err != nil {
		return err
	}
	st, err := in.Complete(ctx, rt)
	if err != nil {
		return err
	}
	if !st.Complete {
		return &incompleteError{name: in.Name, reason: st.Reason, tail: tail(res)}
	}
	return nil
}

// incompleteError is an installer script that finished without a complete
// install.
type incompleteError struct {
	name   string
	reason string
	tail   []string
}

func (e *incompleteError) Error() string {
	msg := "the " + e.name + " installer finished, but the install isn't complete"
	if e.reason != "" {
		msg += " (" + e.reason + ")"
	}
	if len(e.tail) == 0 {
		return msg
	}
	return msg + ". Its output ended with:\n  " + strings.Join(e.tail, "\n  ")
}

// tailLines is how many lines of a script's output an error keeps.
const tailLines = 5

// tail returns the last lines of res's error output, or of its output if
// it wrote no errors, leaving out blank lines.
func tail(res system.Result) []string {
	out := res.Stderr
	if strings.TrimSpace(string(out)) == "" {
		out = res.Stdout
	}
	var lines []string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines[max(0, len(lines)-tailLines):]
}

// script is a downloaded installer script.
type script struct {
	path string // where it's saved
	sum  string // its sha256, in hex
}

// remove deletes the script, if it was saved.
func (s *script) remove() {
	if s.path != "" {
		_ = os.Remove(s.path)
	}
}

// download downloads the script into s, checking its sha256 if there's one
// to check it against. The script is saved in maisys's cache folder.
func (in *Installer) download(ctx context.Context, rt *resource.Runtime, s *script) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return err
	}
	client := in.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't download the %s installer: %w", in.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't download the %s installer from %s: %s", in.Name, in.URL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxScriptSize+1))
	if err != nil {
		return fmt.Errorf("couldn't download the %s installer from %s: %w", in.Name, in.URL, err)
	}
	if len(data) > maxScriptSize {
		return fmt.Errorf("the %s installer at %s is larger than %d MiB", in.Name, in.URL, maxScriptSize>>20)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if in.SHA256 != "" && !strings.EqualFold(got, in.SHA256) {
		return fmt.Errorf("%w: %s has sha256 %s, not %s", errChecksum, in.URL, got, strings.ToLower(in.SHA256))
	}

	dir := cmp.Or(rt.Paths.CacheDir, os.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "installer-"+in.Name+"-*.sh")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("couldn't save the %s installer: %w", in.Name, err)
	}
	s.path, s.sum = f.Name(), got
	return nil
}

// sleep waits for d, or until ctx is done.
func (in *Installer) sleep(ctx context.Context, d time.Duration) error {
	if in.Sleep != nil {
		return in.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// log writes a note to the run's log.
func (in *Installer) log(rt *resource.Runtime, format string, args ...any) {
	if rt.Log != nil {
		_, _ = fmt.Fprintf(rt.Log, "installer %s: %s\n", in.Name, fmt.Sprintf(format, args...))
	}
}

// Remover handles the records of installs taken out of the config. The
// removal rules leave installs alone, so it reports them as no longer used
// and keeps their records.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "installer".
func (Remover) Kind() string { return Kind }

// CheckRemove reports the install as no longer used: it isn't removed.
func (Remover) CheckRemove(_ context.Context, _ *resource.Runtime, rec state.Record) (resource.Change, error) {
	name := rec.Details["name"]
	if name == "" {
		name, _ = strings.CutPrefix(rec.ID, Kind+":")
	}
	return resource.Change{Action: resource.Orphan, Summary: name}, nil
}

// Remove does nothing: installs aren't removed.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }

// tilde returns path with the home folder shown as ~.
func tilde(home, path string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}
