// Package buildinfo reports the version of the running maisys binary.
//
// Release builds set the values with -ldflags "-X", for example:
//
//	-X github.com/plombardi89/maisys2/internal/buildinfo.version=0.1.0
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// DevVersion is the version of builds that aren't releases.
const DevVersion = "dev"

var (
	version = DevVersion
	commit  = ""
	date    = ""

	// updateRepo is the GitHub repo (owner/name) self-update installs
	// releases from. Release builds set it to the repo that built them (see
	// .goreleaser.yaml), so forks update from their own releases.
	updateRepo = "plombardi89/maisys2"
)

// Version returns the release version, or DevVersion for builds that aren't
// releases.
func Version() string {
	return version
}

// UpdateRepo returns the GitHub repo (owner/name) that self-update installs
// releases from.
func UpdateRepo() string {
	return updateRepo
}

// IsDev reports whether this is a development build rather than a release.
func IsDev() bool {
	return version == DevVersion
}

// String describes the build on one line, for example
// "0.1.0 (commit 1a2b3c4, built 2026-11-06T05:31:35Z)".
func String() string {
	c, d := commit, date
	if c == "" {
		c, d = vcsInfo()
	}

	var details []string
	if c != "" {
		details = append(details, "commit "+c)
	}
	if d != "" {
		details = append(details, "built "+d)
	}
	if len(details) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(details, ", "))
}

// vcsInfo falls back to the version control details Go embeds in builds made
// without -ldflags, such as `go build` or `go install`.
func vcsInfo() (revision, when string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if revision != "" && modified {
		revision += "-dirty"
	}
	return revision, when
}
