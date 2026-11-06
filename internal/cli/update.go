package cli

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/selfupdate"
)

// selfUpdatedEnv is set when maisys restarts itself after an update, so the
// new binary doesn't check for updates again.
const selfUpdatedEnv = "MAISYS_SELF_UPDATED"

// updateSelf is the first step of sync. If a newer release exists, it
// installs it and runs the same command again with the new binary. Any
// failure is only a warning, so the sync carries on with this binary.
func updateSelf(cmd *cobra.Command, g *globalOptions) error {
	errOut := cmd.ErrOrStderr()
	if buildinfo.IsDev() {
		g.debugf(errOut, "not checking for a newer maisys: this is a development build")
		return nil
	}
	if g.getenv(selfUpdatedEnv) != "" {
		return nil
	}

	current := buildinfo.Version()
	warn := func(format string, a ...any) {
		_, _ = fmt.Fprintf(errOut, "maisys: warning: "+format+"\n", a...)
	}

	exe, err := selfupdate.Executable()
	if err != nil {
		warn("couldn't update maisys: %v (carrying on with %s)", err, current)
		return nil
	}
	releaseURL := g.getenv("MAISYS_RELEASE_URL")
	if releaseURL == "" {
		releaseURL = selfupdate.DefaultReleaseURL(buildinfo.UpdateRepo())
	}
	u := &selfupdate.Updater{
		ReleaseURL: releaseURL,
		Current:    current,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Executable: exe,
		Client:     &http.Client{Timeout: 5 * time.Minute},
		UserAgent:  "maisys/" + current,
	}

	g.debugf(errOut, "checking %s for a newer maisys", releaseURL)
	rel, updated, err := u.Update(cmd.Context())
	if err != nil {
		warn("couldn't update maisys: %v (carrying on with %s)", err, current)
		return nil
	}
	if !updated {
		g.debugf(errOut, "already up to date (%s)", current)
		return nil
	}

	_, _ = fmt.Fprintf(errOut, "maisys: updated from %s to %s; restarting\n", current, rel.Version)
	code, err := selfupdate.Restart(cmd.Context(), exe, g.args, append(os.Environ(), selfUpdatedEnv+"=1"))
	if err != nil {
		warn("couldn't restart the new maisys: %v (carrying on with %s; the next run uses %s)", err, current, rel.Version)
		return nil
	}
	return exitCodeError{code: code}
}
