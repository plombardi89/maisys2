package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

// ReadMarker reads the repo's maisys.toml. A config_version newer than
// this maisys reads is reported before anything else, since the file may
// then have keys and values this maisys doesn't know.
func (r *Repo) ReadMarker() (*Marker, error) {
	return r.readMarker(ConfigVersion)
}

// readMarker is ReadMarker for a maisys whose current config_version is
// current.
func (r *Repo) readMarker(current int) (*Marker, error) {
	data, err := fs.ReadFile(r.FS, markerFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s doesn't exist, so %w", markerFile, ErrNotConfigRepo)
	}
	if err != nil {
		return nil, err
	}
	if err := newerConfig(data, current); err != nil {
		return nil, err
	}
	mk := &Marker{}
	present, err := decode(markerFile, data, reflect.ValueOf(mk), r.Strict, nil)
	if err != nil {
		return nil, err
	}
	if _, ok := present["config_version"]; !ok {
		return nil, fmt.Errorf("%s: config_version is missing: set it to %d", markerFile, ConfigVersion)
	}
	mk.locs = locate(data)
	return mk, nil
}

// newerConfig returns an error if data, the contents of maisys.toml, sets
// a config_version newer than current, the one this maisys reads. It
// leaves any other problem to decoding.
func newerConfig(data []byte, current int) error {
	var keys map[string]any
	if err := toml.Unmarshal(data, &keys); err == nil {
		if v, ok := keys["config_version"].(int64); ok && v > int64(current) {
			return tooNew(locate(data).where(markerFile, "config_version"), v, current)
		}
	}
	return nil
}

// tooNew is the error for config_version v, set at at, which a maisys
// whose current config_version is current can't read.
func tooNew(at string, v int64, current int) error {
	return fmt.Errorf("%s: config_version %d needs a newer maisys: this one reads config_version %s; update maisys", at, v, reads(current))
}

// reads describes the config_versions a maisys whose current
// config_version is current reads, such as "2 and 3".
func reads(current int) string {
	if oldest := max(current-1, 1); oldest < current {
		return fmt.Sprintf("%d and %d", oldest, current)
	}
	return fmt.Sprint(current)
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
	switch v := mk.ConfigVersion; {
	case v < 1:
		errs = append(errs, fmt.Errorf("%s: config_version must be 1 or more, not %d", at, v))
	case v > current:
		errs = append(errs, tooNew(at, int64(v), current))
	case v < oldest:
		errs = append(errs, fmt.Errorf("%s: config_version %d is too old for this maisys, which reads config_version %s: migrate the config (the release notes say how)", at, v, reads(current)))
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
