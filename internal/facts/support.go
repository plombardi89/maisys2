package facts

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
)

// The rolling support policy: the Fedora releases Fedora currently
// supports, the two newest Ubuntu LTS releases, and Windows 11. When a
// release ships or reaches end of life, change fedoraReleases or ubuntuLTS
// together with the test matrix in .github/workflows/ci.yml (and the main
// ruleset's required checks) and the list in AGENTS.md. TestPolicyMatchesCI
// checks that the lists and the matrix agree.

// fedoraRelease is a Fedora release and the day it reaches end of life.
type fedoraRelease struct {
	version string
	eol     time.Time
}

// fedoraReleases are recent Fedora releases, oldest first, each supported
// until its end of life. The dates follow
// https://docs.fedoraproject.org/en-US/releases/lifecycle/.
var fedoraReleases = []fedoraRelease{
	{"42", day(2026, time.May, 13)},
	{"43", day(2026, time.December, 9)},
	{"44", day(2027, time.May, 19)},
	{"45", day(2027, time.December, 1)},
}

// ubuntuLTS are the two newest Ubuntu LTS releases, oldest first.
var ubuntuLTS = []string{"24.04", "26.04"}

// windows11Build is the first Windows 11 build.
const windows11Build = 22000

// runsOn says which operating systems maisys runs on.
const runsOn = "maisys runs on Fedora, Ubuntu and Windows 11"

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

// Support is what the rolling support policy says about a system.
type Support struct {
	// Supported reports whether maisys supports the system: a Fedora
	// release that hasn't reached end of life, one of the two newest Ubuntu
	// LTS releases, or Windows 11, on an architecture maisys runs on.
	Supported bool
	// Reason says why the system isn't supported, as a sentence for an
	// "untested" warning, such as "Ubuntu 25.10 isn't one of the LTS
	// releases maisys tests on (24.04 and 26.04); maisys may still work".
	// It's "" when Supported is true.
	Reason string
}

// Support returns what the rolling support policy says about s on the day
// now. A Fedora or Ubuntu release outside the policy, or a Windows build
// older than Windows 11, only gets a warning: the sync still runs. An OS or
// architecture maisys doesn't run on at all isn't supported either, and
// CheckMachine reports it as an error.
func (s *System) Support(now time.Time) Support {
	switch {
	case !slices.Contains(config.AllOS(), s.OS):
		return Support{Reason: fmt.Sprintf("%s, but this is %s", runsOn, s.PrettyName)}
	case s.Arch == "":
		return Support{Reason: fmt.Sprintf("maisys runs on %s, but this system is %s", list(config.AllArch(), "and"), s.RawArch)}
	case s.OS == config.Windows && s.Arch != config.AMD64:
		return Support{Reason: fmt.Sprintf("Windows hosts must be %s, but this system is %s", config.AMD64, s.Arch)}
	case s.OS == config.Fedora:
		return fedoraSupport(s.OSVersion, now)
	case s.OS == config.Ubuntu:
		if slices.Contains(ubuntuLTS, s.OSVersion) {
			return Support{Supported: true}
		}
		return Support{Reason: fmt.Sprintf("%s isn't one of the LTS releases maisys tests on (%s); maisys may still work", s.name(), list(ubuntuLTS, "and"))}
	default:
		if build, err := strconv.ParseUint(s.OSVersion, 10, 32); err == nil && build >= windows11Build {
			return Support{Supported: true}
		}
		return Support{Reason: fmt.Sprintf("%s isn't supported: maisys needs Windows 11 (build %d or newer)", s.name(), windows11Build)}
	}
}

// fedoraSupport is Support for a Fedora release.
func fedoraSupport(version string, now time.Time) Support {
	name := release(config.Fedora, version)
	var tested []string
	for _, r := range fedoraReleases {
		switch {
		case r.version == version && now.Before(r.eol):
			return Support{Supported: true}
		case r.version == version:
			return Support{Reason: fmt.Sprintf("%s isn't supported any more (end of life %s); maisys may still work", name, r.eol.Format(time.DateOnly))}
		case now.Before(r.eol):
			tested = append(tested, r.version)
		default:
		}
	}
	if len(tested) == 0 {
		return Support{Reason: fmt.Sprintf("%s isn't one of the releases maisys tests on; maisys may still work", name)}
	}
	return Support{Reason: fmt.Sprintf("%s isn't one of the releases maisys tests on (%s); maisys may still work", name, list(tested, "and"))}
}

// name names the OS release s runs, as release does, or by its PrettyName
// if maisys doesn't support it.
func (s *System) name() string {
	if s.OS == "" {
		return s.PrettyName
	}
	return release(s.OS, s.OSVersion)
}

// release names a release of the OS id for people to read, such as "Fedora 44",
// "Ubuntu 26.04" or "Windows 11 (build 26100)". version may be "".
func release(id config.OS, version string) string {
	name := string(id)
	switch id {
	case config.Fedora:
		name = "Fedora"
	case config.Ubuntu:
		name = "Ubuntu"
	case config.Windows:
		if build, err := strconv.ParseUint(version, 10, 32); err == nil {
			return windowsName(uint32(build))
		}
		name = "Windows"
	default:
	}
	return strings.TrimSpace(name + " " + version)
}

// list joins items as "a, b and c", with conj before the last.
func list[T ~string](items []T, conj string) string {
	s := make([]string, len(items))
	for i, item := range items {
		s[i] = string(item)
	}
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " " + conj + " " + s[len(s)-1]
}
