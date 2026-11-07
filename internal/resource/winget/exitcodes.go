package winget

import (
	"errors"
	"fmt"
	"strings"

	"github.com/plombardi89/maisys2/internal/system"
)

// winget's exit codes are HRESULTs, which Windows reports as unsigned
// 32-bit exit codes. These are the ones maisys acts on, from winget's
// documentation (doc/windows/package-manager/winget/returnCodes.md in
// microsoft/winget-cli).
const (
	// invalidArguments is what winget exits with for a command it doesn't
	// have, such as dscv3 before it had it.
	invalidArguments uint32 = 0x8A150002 // APPINSTALLER_CLI_ERROR_INVALID_CL_ARGUMENTS
	// noApplicationsFound means no package matched: show and dscv3 for
	// an ID the source doesn't have, export when nothing installed comes
	// from the source, upgrade for a package that isn't installed.
	noApplicationsFound uint32 = 0x8A150014 // APPINSTALLER_CLI_ERROR_NO_APPLICATIONS_FOUND
	// commandRequiresAdmin is install --scope machine without admin
	// rights.
	commandRequiresAdmin uint32 = 0x8A150019 // APPINSTALLER_CLI_ERROR_COMMAND_REQUIRES_ADMIN
	// experimentalFeatureDisabled is a command that's still an
	// experimental feature in this winget, as dscv3 was at first.
	experimentalFeatureDisabled uint32 = 0x8A15001D // APPINSTALLER_CLI_ERROR_EXPERIMENTAL_FEATURE_DISABLED
	// updateNotApplicable means upgrade found nothing newer to install,
	// as can install of a package that's already installed.
	updateNotApplicable uint32 = 0x8A15002B // APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE
	// upgradeVersionNotNewer means upgrade found nothing newer than
	// what's installed.
	upgradeVersionNotNewer uint32 = 0x8A15004F // APPINSTALLER_CLI_ERROR_UPGRADE_VERSION_NOT_NEWER
	// packageAlreadyInstalled is install of a package that's already
	// installed.
	packageAlreadyInstalled uint32 = 0x8A150061 // APPINSTALLER_CLI_ERROR_PACKAGE_ALREADY_INSTALLED
	// rebootRequiredToFinish means the package is installed, but Windows
	// must restart to finish. Recent wingets exit with 0 for it, and only
	// say so; older ones exit with this.
	rebootRequiredToFinish uint32 = 0x8A150109 // APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_REQUIRED_TO_FINISH
	// rebootRequiredForInstall means the installer needs Windows to
	// restart before it can install.
	rebootRequiredForInstall uint32 = 0x8A15010A // APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_REQUIRED_FOR_INSTALL
	// rebootInitiated means the installer is restarting Windows to finish.
	rebootInitiated uint32 = 0x8A15010B // APPINSTALLER_CLI_ERROR_INSTALL_REBOOT_INITIATED
)

// messages are winget's descriptions of the exit codes an install or
// upgrade is most likely to fail with, for errors.
var messages = map[uint32]string{
	0x8A150008:                  "downloading the installer failed",
	0x8A150010:                  "none of the package's installers is applicable to this system",
	0x8A150011:                  "the installer's hash doesn't match the manifest",
	noApplicationsFound:         "no package found",
	commandRequiresAdmin:        "it needs admin rights",
	0x8A15002D:                  "the installer failed a security check",
	0x8A15003A:                  "Group Policy blocks it",
	0x8A150041:                  "the package's agreements weren't accepted",
	0x8A150046:                  "the source's agreements weren't accepted",
	0x8A150056:                  "the installer can't run with admin rights",
	0x8A150068:                  "the package is pinned (see winget pin list)",
	0x8A15008E:                  "the upgrade uses a different install technology than the installed version",
	0x8A150101:                  "the application is running: exit it, then try again",
	0x8A150102:                  "another installation is in progress: try again later",
	0x8A150103:                  "a file is in use: exit the application, then try again",
	0x8A150104:                  "a dependency is missing from the system",
	0x8A150105:                  "the disk is full",
	0x8A150106:                  "there isn't enough memory",
	0x8A150107:                  "the installer needs a network connection",
	0x8A150108:                  "the installer failed: contact its support",
	rebootRequiredToFinish:      "restart Windows to finish the installation",
	rebootRequiredForInstall:    "restart Windows, then try again",
	rebootInitiated:             "Windows is restarting to finish the installation",
	0x8A15010C:                  "the installation was cancelled",
	0x8A15010D:                  "another version of the application is installed",
	0x8A15010E:                  "a newer version of the application is installed",
	0x8A15010F:                  "organization policies prevent the installation",
	0x8A150110:                  "installing the package's dependencies failed",
	0x8A150111:                  "another application is using the application",
	0x8A150113:                  "the package doesn't support this system",
	0x8A150114:                  "the installer can't upgrade an installed version",
	0x8A150115:                  "the installer failed with an error of its own",
	experimentalFeatureDisabled: "the command is an experimental feature in this winget",
}

// exitCode returns the exit code a command run by the system.Runner
// reports for code, an unsigned exit code.
func exitCode(code uint32) int {
	return int(code)
}

// exitCodes returns the exit codes a command run by the system.Runner
// reports for codes, for system.Cmd.AllowExit.
func exitCodes(codes ...uint32) []int {
	ints := make([]int, len(codes))
	for i, c := range codes {
		ints[i] = exitCode(c)
	}
	return ints
}

// is reports whether res exited with code.
func is(res system.Result, code uint32) bool {
	return res.ExitCode == exitCode(code)
}

// CommandError is a winget command that failed with an exit code it
// doesn't allow.
type CommandError struct {
	// Label says what failed, such as "winget install Git.Git".
	Label string
	// Code is winget's exit code.
	Code uint32
	// Tail is the last lines of winget's output.
	Tail []string
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("%s failed with exit code 0x%08X", e.Label, e.Code)
	if m := messages[e.Code]; m != "" {
		msg += ": " + m
	}
	if len(e.Tail) > 0 {
		msg += "\n  " + strings.Join(e.Tail, "\n  ")
	}
	return msg
}

// RebootError means Windows must restart: to finish installing or
// upgrading a package, or before it can be. The sync stops and asks for a
// restart when it finds one (see RebootRequired).
type RebootError struct {
	// ID is the package's ID.
	ID string
	// Code is winget's exit code, one of the reboot ones.
	Code uint32
}

func (e *RebootError) Error() string {
	switch e.Code {
	case rebootRequiredForInstall:
		return fmt.Sprintf("Windows must restart before winget can install %s (winget exit code 0x%08X)", e.ID, e.Code)
	case rebootInitiated:
		return fmt.Sprintf("Windows is restarting to finish installing %s (winget exit code 0x%08X)", e.ID, e.Code)
	default:
		return fmt.Sprintf("Windows must restart to finish installing %s (winget exit code 0x%08X)", e.ID, e.Code)
	}
}

// RebootRequired reports true: the sync must stop, and ask for Windows to
// restart and maisys sync to run again. The sync finds it with errors.As,
// through any error that wraps it, with an interface of this one method.
func (e *RebootError) RebootRequired() bool { return true }

// commandError turns an error from running a winget command into a
// *CommandError if winget exited with a code it doesn't allow, or a
// *RebootError for a reboot one. Other errors are returned as they are.
func commandError(id string, err error) error {
	ee, ok := errors.AsType[*system.ExitError](err)
	if !ok {
		return err
	}
	code := uint32(ee.Code)
	switch code {
	case rebootRequiredToFinish, rebootRequiredForInstall, rebootInitiated:
		return &RebootError{ID: id, Code: code}
	}
	return &CommandError{Label: ee.Label, Code: code, Tail: ee.Tail}
}
