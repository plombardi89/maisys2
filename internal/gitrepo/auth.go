package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrAuth is wrapped by errors from git failing to sign in to a repo. Their
// messages say how to sign in.
var ErrAuth = errors.New("git couldn't sign in")

// Auth is how maisys signs in to an HTTPS config repo when git's own sign-in
// isn't enough.
type Auth struct {
	// Token is an access token from MAISYS_CONFIG_TOKEN. git gets it through
	// its environment, for each command, and it's never written to disk.
	Token string
	// CredentialsFile is where a token typed at the prompt is saved
	// (~/.config/maisys/git-credentials).
	CredentialsFile string
	// Prompt asks for a read-only access token for url. It's nil when
	// there's no terminal to ask in.
	Prompt func(ctx context.Context, url string) (string, error)
}

// tokenKind describes the token maisys needs.
const tokenKind = "for GitHub, a fine-grained token with Contents: read on that one repo"

// tokenEnv is the environment variable that carries Auth.Token to
// tokenHelper. It's only set for the git command that needs the token.
const tokenEnv = "MAISYS_GIT_TOKEN"

// tokenHelper is a git credential helper that answers with the token in
// tokenEnv. git runs it with sh, where printf is a builtin, so the token
// never appears on a command line.
const tokenHelper = `!f() { if [ "$1" = get ]; then echo username=maisys; printf 'password=%s\n' "$` + tokenEnv + `"; fi; }; f`

// tokenCall makes c sign in with a.Token, and with no other credential
// helper.
func (a *Auth) tokenCall(c call) (call, error) {
	if err := checkToken("MAISYS_CONFIG_TOKEN", a.Token); err != nil {
		return c, err
	}
	c.config = append(c.config, "credential.helper=", "credential.helper="+tokenHelper)
	c.env = append(c.env, tokenEnv+"="+a.Token)
	return c, nil
}

// storeHelper is git's built-in credential helper that reads the
// credentials saved in file.
func storeHelper(file string) string {
	return "store --file " + shellQuote(filepath.ToSlash(file))
}

// useCredentialsFile sets up the clone to sign in with the credentials saved
// in file, and with no other credential helper, so the token is only ever
// used for this clone.
func (r *Repo) useCredentialsFile(ctx context.Context, file string) error {
	for _, args := range [][]string{
		{"config", "--local", "--replace-all", "credential.helper", ""},
		{"config", "--local", "--add", "credential.helper", storeHelper(file)},
	} {
		if _, err := r.run(ctx, call{dir: r.Dir, args: args}); err != nil {
			return err
		}
	}
	return nil
}

// credentialsFile returns the file of saved credentials the clone is set up
// to use, or "" if there isn't one.
func (r *Repo) credentialsFile(ctx context.Context) string {
	out, err := r.run(ctx, call{dir: r.Dir, args: []string{"config", "--local", "--get-all", "credential.helper"}})
	if err != nil {
		return ""
	}
	for line := range strings.Lines(out) {
		if file, ok := strings.CutPrefix(strings.TrimSpace(line), "store --file "); ok {
			return filepath.FromSlash(shellUnquote(file))
		}
	}
	return ""
}

// SaveToken saves token in file as the credentials for the host in rawURL,
// replacing any saved for that host before. The file is in the format of
// git's built-in "store" credential helper, and only its owner can read it.
func SaveToken(file, rawURL, token string) error {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("can't save a token for %s: it isn't an HTTPS URL", Redact(rawURL))
	}
	if err := checkToken("the access token", token); err != nil {
		return err
	}
	user := "maisys"
	if u.User != nil && u.User.Username() != "" {
		user = u.User.Username()
	}

	var lines []string
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("couldn't read the saved tokens: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" || sameHost(line, u.Host) {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, "https://"+escapeCredential(user)+":"+escapeCredential(token)+"@"+u.Host)

	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("couldn't save the token: %w", err)
	}
	if err := writePrivate(file, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return fmt.Errorf("couldn't save the token: %w", err)
	}
	return nil
}

// writePrivate replaces file with data, readable only by its owner.
func writePrivate(file string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once it's been renamed

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, file)
}

// sameHost reports whether a line of saved credentials is for an HTTPS URL
// on host. git's store helper percent-encodes the host's port separator,
// which url.Parse rejects, so the line is taken apart here.
func sameHost(line, host string) bool {
	scheme, rest, ok := strings.Cut(line, "://")
	if !ok || !strings.EqualFold(scheme, "https") {
		return false
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	lineHost, err := url.PathUnescape(rest)
	return err == nil && strings.EqualFold(lineHost, host)
}

// escapeCredential percent-encodes s the way git's store helper does, leaving
// only RFC 3986's unreserved characters as they are.
func escapeCredential(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02x", c)
		}
	}
	return b.String()
}

func checkToken(name, token string) error {
	if token == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("%s can't contain spaces or line breaks", name)
	}
	return nil
}

// authMessages are parts of git's (and common git hosts') messages when it
// can't sign in, in lower case.
var authMessages = []string{
	"could not read username",
	"could not read password",
	"terminal prompts disabled",
	"authentication failed",
	"the requested url returned error: 401",
	"the requested url returned error: 403",
	"http basic: access denied",
	"invalid username or password",
	"invalid username or token",
	"permission denied (publickey",
}

// authFailed reports whether git's error output says it couldn't sign in.
func authFailed(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, m := range authMessages {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// repoNotFound reports whether git's error output says the repo doesn't
// exist. Hosts such as GitHub say that when a token can't read a private
// repo.
func repoNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "repository not found") ||
		strings.Contains(s, "the requested url returned error: 404") ||
		(strings.Contains(s, "repository '") && strings.Contains(s, "' not found"))
}

// signInOptions lists the ways to sign in to an HTTPS repo. withPrompt
// includes the token prompt.
func signInOptions(withPrompt bool) string {
	opts := []string{"set MAISYS_CONFIG_TOKEN to a read-only access token for the repo (" + tokenKind + ")"}
	if withPrompt {
		opts = append(opts, "run maisys in a terminal, where it asks for a token")
	}
	opts = append(opts, "use the repo's SSH URL instead, which signs in with your SSH key")
	return "  - " + strings.Join(opts, "\n  - ")
}

// tokenRejected is the error for a token git couldn't sign in with. what
// says where the token came from.
func tokenRejected(gitMessage, shown, what string) error {
	return fmt.Errorf("%w to %s with %s (%s).\nThe token needs read access to the repo: %s",
		ErrAuth, shown, what, gitMessage, tokenKind)
}

// otherAuthError is the error for a repo that isn't on HTTPS when git
// couldn't sign in to it.
func otherAuthError(rawURL string, ge *gitError) error {
	fix := "Use the repo's HTTPS URL, or its SSH URL to sign in with your SSH key"
	if isSSH(rawURL) {
		fix = "Check that your SSH key is loaded (ssh-add -l lists them) and can read the repo, or use the repo's HTTPS URL"
	}
	return fmt.Errorf("%w to %s (%s).\n%s", ErrAuth, Redact(rawURL), ge.message(), fix)
}

// pullAuthError is the error for a pull that couldn't sign in, usually
// because the credentials have expired.
func (r *Repo) pullAuthError(ctx context.Context, rawURL string, ge *gitError, withToken bool) error {
	var fix string
	switch {
	case withToken:
		fix = "The token in MAISYS_CONFIG_TOKEN was rejected: it may have expired, or it can't read the repo. Set it to a new read-only access token (" + tokenKind + ")."
	case !isHTTPS(rawURL):
		fix = "Check that your SSH key is loaded (ssh-add -l lists them) and can still read the repo."
	default:
		if file := r.credentialsFile(ctx); file != "" {
			fix = "The token saved in " + file + " was rejected: it may have expired or been revoked. Create a new read-only access token (" + tokenKind + ") and set MAISYS_CONFIG_TOKEN to it."
		} else {
			fix = "The saved credentials may have expired. Sign in again with your git credential helper, or set MAISYS_CONFIG_TOKEN to a read-only access token (" + tokenKind + ")."
		}
	}
	return fmt.Errorf("%w to %s to pull the config repo (%s).\n%s\nRun \"maisys sync --local\" to carry on with the copy you already have",
		ErrAuth, Redact(rawURL), ge.message(), fix)
}

func isHTTPS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != ""
}

// isSSH reports whether rawURL is an ssh:// URL or scp-like (host:path).
func isSSH(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, scheme := range []string{"ssh://", "git+ssh://", "ssh+git://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	if strings.Contains(rawURL, "://") {
		return false
	}
	host, _, ok := strings.Cut(rawURL, ":")
	// A single letter is a Windows drive, as in C:\config.
	return ok && len(host) > 1 && !strings.ContainsAny(host, `/\`)
}

// Redact hides any credentials in a URL, so it can be shown.
func Redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = url.User("xxxxx")
	return u.String()
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellUnquote undoes shellQuote.
func shellUnquote(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") {
		return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
	}
	return s
}
