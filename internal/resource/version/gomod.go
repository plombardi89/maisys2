package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/module"

	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/system"
)

// DefaultGoProxy is GOPROXY when it isn't set.
const DefaultGoProxy = "https://proxy.golang.org,direct"

// GoModule is the latest version of a Go program installed with go install
// …@latest: the version of the module that provides its package, as the Go
// module proxy has it. It follows your Go settings as go install does,
// from the environment or from the file go env -w writes:
//
//   - GOPROXY lists the proxies to ask, in order: after a comma, the next
//     one is asked only if a proxy doesn't have the module; after a pipe,
//     also if it fails. "direct" means fetching from the module's own
//     repo, and "off" means no lookups at all.
//   - Modules matching GONOPROXY (which defaults to GOPRIVATE) are always
//     fetched from their own repo, so they never reach a proxy.
//
// maisys doesn't talk to version control itself: lookups that go "direct"
// use Direct, which runs go list with the user's Go settings and
// credentials (see GoList). Without it, they fail, saying to pin a
// version.
type GoModule struct {
	// Path is the program's package path, such as
	// "golang.org/x/tools/cmd/goimports". Its module is the longest prefix
	// of Path that's a module.
	Path string
	// Getenv reads environment variables. Nil means os.Getenv.
	Getenv func(string) string
	// Direct looks up the latest version of the module providing a
	// package without a proxy. Nil means such lookups fail.
	Direct func(ctx context.Context, path string) (string, error)
}

// Upstream names the Go module proxy and the package.
func (g GoModule) Upstream() string {
	return "the Go module proxy (" + g.Path + ")"
}

// Latest returns the latest version of the module that provides g.Path,
// with its "v", such as "v0.31.0".
func (g GoModule) Latest(ctx context.Context, c *download.Client) (string, error) {
	env := readGoEnv(g.Getenv)
	if module.MatchPrefixPatterns(env.noProxy, g.Path) {
		return g.direct(ctx, "it matches GONOPROXY or GOPRIVATE")
	}
	var lastErr error
	for _, p := range proxyList(env.proxy) {
		switch p.url {
		case "off":
			if lastErr != nil {
				return "", lastErr
			}
			return "", errors.New("GOPROXY is off, which turns module lookups off")
		case "direct":
			return g.direct(ctx, "GOPROXY says to fetch it directly")
		}
		v, err := proxyLatest(ctx, c, p.url, g.Path)
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !p.onAnyError && !isNotFound(err) {
			return "", err
		}
	}
	if lastErr == nil {
		return "", errors.New("GOPROXY lists no proxy")
	}
	return "", lastErr
}

// direct looks g.Path up without a proxy, for the reason given.
func (g GoModule) direct(ctx context.Context, why string) (string, error) {
	if g.Direct == nil {
		return "", fmt.Errorf("maisys can only look %s up through a module proxy, and %s", g.Path, why)
	}
	return g.Direct(ctx, g.Path)
}

// notFoundError means no module provides a package.
type notFoundError struct {
	proxy, path string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("%s has no module that provides %s", e.proxy, e.path)
}

func isNotFound(err error) bool {
	_, ok := errors.AsType[*notFoundError](err)
	return ok || download.IsNotFound(err)
}

// proxyLatest asks the proxy at base for the latest version of each prefix
// of pkg in turn, from the longest, until one is a module.
func proxyLatest(ctx context.Context, c *download.Client, base, pkg string) (string, error) {
	for _, mod := range prefixes(pkg) {
		escaped, err := module.EscapePath(mod)
		if err != nil {
			continue
		}
		data, err := c.Get(ctx, strings.TrimSuffix(base, "/")+"/"+escaped+"/@latest")
		if download.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		var info struct {
			Version string `json:"Version"`
		}
		if err := json.Unmarshal(data, &info); err != nil {
			return "", fmt.Errorf("reading the module proxy's answer for %s: %w", mod, err)
		}
		return checkVersion(info.Version)
	}
	return "", &notFoundError{proxy: redact(base), path: pkg}
}

// prefixes returns path and each of its prefixes that ends at a slash,
// longest first.
func prefixes(p string) []string {
	var list []string
	for p != "." && p != "/" && p != "" {
		list = append(list, p)
		p = path.Dir(p)
	}
	return list
}

// redact returns a proxy URL without the password it may hold.
func redact(proxy string) string {
	u, err := url.Parse(proxy)
	if err != nil {
		return "a proxy in GOPROXY"
	}
	return u.Redacted()
}

// proxy is an entry of GOPROXY.
type proxy struct {
	url string
	// onAnyError says to fall back to the next entry if this one fails in
	// any way, not just if it doesn't have the module.
	onAnyError bool
}

// proxyList splits GOPROXY into its entries. A URL without a scheme is an
// https one.
func proxyList(goproxy string) []proxy {
	var list []proxy
	for goproxy != "" {
		i := strings.IndexAny(goproxy, ",|")
		entry, sep := goproxy, byte(0)
		if i >= 0 {
			entry, sep, goproxy = goproxy[:i], goproxy[i], goproxy[i+1:]
		} else {
			goproxy = ""
		}
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case entry == "off" || entry == "direct":
		case !strings.Contains(entry, "://"):
			entry = "https://" + entry
		}
		list = append(list, proxy{url: entry, onAnyError: sep == '|'})
	}
	return list
}

// goEnv holds the Go settings lookups follow.
type goEnv struct {
	proxy, noProxy string
}

// readGoEnv reads the Go settings as the go command does: from the
// environment, then from the file go env -w writes, then the defaults.
func readGoEnv(getenv func(string) string) goEnv {
	if getenv == nil {
		getenv = os.Getenv
	}
	file := goEnvFile(getenv)
	get := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return file[key]
	}
	env := goEnv{proxy: get("GOPROXY"), noProxy: get("GONOPROXY")}
	if env.proxy == "" {
		env.proxy = DefaultGoProxy
	}
	if env.noProxy == "" {
		env.noProxy = get("GOPRIVATE")
	}
	return env
}

// goEnvFile reads the settings go env -w saves: $GOENV, or go/env in the
// user's config folder.
func goEnvFile(getenv func(string) string) map[string]string {
	name := getenv("GOENV")
	if name == "off" {
		return nil
	}
	if name == "" {
		dir := getenv("XDG_CONFIG_HOME")
		switch {
		case runtime.GOOS == "windows":
			dir = getenv("AppData")
		case dir == "" && getenv("HOME") != "":
			dir = filepath.Join(getenv("HOME"), ".config")
		}
		if dir == "" {
			return nil
		}
		name = filepath.Join(dir, "go", "env")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil
	}
	settings := map[string]string{}
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" && !strings.HasPrefix(key, "#") {
			settings[key] = value
		}
	}
	return settings
}

// GoList returns a GoModule.Direct that runs "go list -m" with the go
// command goBin, through r: for each prefix of the package path, from the
// longest, until one is a module. That looks the module up as go install
// would, with the user's Go settings and credentials, so it works for
// private modules too.
func GoList(r system.Runner, goBin string) func(ctx context.Context, path string) (string, error) {
	return func(ctx context.Context, pkg string) (string, error) {
		var firstErr error
		for _, mod := range prefixes(pkg) {
			if module.CheckPath(mod) != nil {
				continue
			}
			res, err := r.Run(ctx, system.Cmd{
				Name:  goBin,
				Args:  []string{"list", "-m", "-json", mod + "@latest"},
				Env:   []string{"GO111MODULE=on", "GOWORK=off"},
				Dir:   os.TempDir(),
				Label: "go list -m " + mod + "@latest",
			})
			if _, ok := errors.AsType[*system.ExitError](err); ok && ctx.Err() == nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err != nil {
				return "", err
			}
			var info struct {
				Version string `json:"Version"`
			}
			if err := json.Unmarshal(res.Stdout, &info); err != nil {
				return "", fmt.Errorf("reading go list's answer for %s: %w", mod, err)
			}
			return checkVersion(info.Version)
		}
		if firstErr == nil {
			return "", fmt.Errorf("%s isn't a valid package path", pkg)
		}
		return "", firstErr
	}
}
