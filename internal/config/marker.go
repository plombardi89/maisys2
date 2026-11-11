package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"

	"golang.org/x/mod/semver"
)

// ConfigVersion is the current version of the config format. maisys reads
// configs at this version and the one before it.
const ConfigVersion = 1

// markerFile is the file that marks the top of a config repo.
const markerFile = "maisys.toml"

// ErrNotConfigRepo is wrapped by the error ReadMarker returns when the repo
// has no maisys.toml.
var ErrNotConfigRepo = errors.New("this isn't a maisys config repo")

// Marker is maisys.toml, which marks the top of a config repo and says which
// versions of maisys can read it.
type Marker struct {
	// ConfigVersion is the version of the config format the repo uses.
	ConfigVersion int `toml:"config_version"`
	// Requires lists the maisys versions the config needs, such as
	// ">= 0.6" or ">= 0.6, < 2". Without it, any maisys that reads
	// config_version will do.
	Requires string `toml:"requires"`

	locs *locations
}

// ReadMarker reads the repo's maisys.toml.
func (r *Repo) ReadMarker() (*Marker, error) {
	data, err := fs.ReadFile(r.FS, markerFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s doesn't exist, so %w", markerFile, ErrNotConfigRepo)
	}
	if err != nil {
		return nil, err
	}
	mk := &Marker{}
	present, err := decode(markerFile, data, reflect.ValueOf(mk), r.Strict)
	if err != nil {
		return nil, err
	}
	if _, ok := present["config_version"]; !ok {
		return nil, fmt.Errorf("%s: config_version is missing: set it to %d", markerFile, ConfigVersion)
	}
	mk.locs = locate(data)
	return mk, nil
}

// Check checks that this maisys, whose version is maisysVersion, can read
// the config: that it reads config_version, and meets requires. It returns
// warnings for a config at the previous config_version, which should be
// migrated, and for a development build, which can't check requires.
func (mk *Marker) Check(maisysVersion string) (warnings []string, err error) {
	return mk.check(maisysVersion, ConfigVersion)
}

// check is Check for a maisys whose current config_version is current.
func (mk *Marker) check(maisysVersion string, current int) ([]string, error) {
	var warnings []string
	var errs []error
	at := mk.locs.where(markerFile, "config_version")
	oldest := max(current-1, 1)
	reads := fmt.Sprint(current)
	if oldest < current {
		reads = fmt.Sprintf("%d and %d", oldest, current)
	}
	switch v := mk.ConfigVersion; {
	case v < 1:
		errs = append(errs, fmt.Errorf("%s: config_version must be 1 or more, not %d", at, v))
	case v > current:
		errs = append(errs, fmt.Errorf("%s: config_version %d needs a newer maisys: this one reads config_version %s; update maisys", at, v, reads))
	case v < oldest:
		errs = append(errs, fmt.Errorf("%s: config_version %d is too old for this maisys, which reads config_version %s: migrate the config (the release notes say how)", at, v, reads))
	case v < current:
		warnings = append(warnings, fmt.Sprintf("%s: config_version %d is the previous version: migrate the config to config_version %d (the release notes say how)", at, v, current))
	default:
	}

	at = mk.locs.where(markerFile, "requires")
	reqs, err := parseRequires(mk.Requires)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("%s: %w", at, err))
	case len(reqs) == 0:
	case !semver.IsValid(canonicalVersion(maisysVersion)):
		warnings = append(warnings, fmt.Sprintf("%s: this maisys (%s) isn't a release, so requires = %q isn't checked", at, maisysVersion, mk.Requires))
	default:
		if unmet, newer := checkRequires(reqs, canonicalVersion(maisysVersion)); unmet {
			msg := fmt.Sprintf("%s: this config needs maisys %s, but this is maisys %s", at, mk.Requires, maisysVersion)
			if newer {
				msg += "; update maisys"
			}
			errs = append(errs, errors.New(msg))
		}
	}
	return warnings, errors.Join(errs...)
}

// requirement is one term of requires: an operator and a version, with a
// leading "v" as semver expects.
type requirement struct {
	op, version string
}

// requireOps are the operators requires allows. Longer ones come first, so
// ">=" isn't read as ">" followed by "=".
var requireOps = []string{">=", "<=", "!=", ">", "<", "="}

// parseRequires parses requires: comma-separated terms, each an optional
// operator (">=" if left out) and a version.
func parseRequires(s string) ([]requirement, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	const hint = `write it like ">= 0.6" or ">= 0.6, < 2"`
	var reqs []requirement
	for term := range strings.SplitSeq(s, ",") {
		term = strings.TrimSpace(term)
		op := ">="
		for _, o := range requireOps {
			if rest, ok := strings.CutPrefix(term, o); ok {
				op, term = o, strings.TrimSpace(rest)
				break
			}
		}
		if term == "" {
			return nil, fmt.Errorf("requires = %q has a requirement with no version: %s", s, hint)
		}
		v := canonicalVersion(term)
		if !semver.IsValid(v) {
			return nil, fmt.Errorf("requires = %q isn't valid: %q isn't a version: %s", s, term, hint)
		}
		reqs = append(reqs, requirement{op: op, version: v})
	}
	return reqs, nil
}

// checkRequires reports whether version fails any of reqs, and if so,
// whether a newer version would meet every one it fails.
func checkRequires(reqs []requirement, version string) (unmet, newer bool) {
	newer = true
	for _, r := range reqs {
		c := semver.Compare(version, r.version)
		var ok bool
		switch r.op {
		case ">=":
			ok = c >= 0
		case ">":
			ok = c > 0
		case "<=":
			ok = c <= 0
		case "<":
			ok = c < 0
		case "=":
			ok = c == 0
		case "!=":
			ok = c != 0
		default:
		}
		if !ok {
			unmet = true
			newer = newer && (r.op == ">=" || r.op == ">" || (r.op == "=" && c < 0))
		}
	}
	return unmet, unmet && newer
}

// canonicalVersion adds the leading "v" semver expects.
func canonicalVersion(v string) string {
	return "v" + strings.TrimPrefix(v, "v")
}
