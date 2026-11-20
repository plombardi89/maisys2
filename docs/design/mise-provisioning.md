# One-command workstation provisioning with mise

**Status: proposed for review.** Keep `maisys sync` as the entry point for a new machine. Use native mise configuration for the packages, tools and files it should receive. Keep the operating-system work that gets a machine to that point in maisys, particularly the Windows-to-WSL handoff. This changes [the design in issue #1](https://github.com/plombardi89/maisys2/issues/1); the commands and inventory below describe the proposal, not a released feature.

The operator should be able to launch a Fedora or Ubuntu VM and find their tools ready on first login. On a Windows machine that already has WSL and a signed-in Git account, one PowerShell command should produce a named distro with the same tools, without opening a guest setup prompt. Updating one shared tool should mean one config-repo change, not a maisys release.

### Why change course?

The [Fedora 44 spike](../spikes/fedora44-mise/README.md) already did the Linux work with a [small config](../spikes/fedora44-mise/config/). A clean x86_64 guest installed five locked tools, DNF packages, RPM Fusion, shell plugins and dotfiles in 48 seconds **from invoking mise**. After reboot, Bash, Zsh and Fish found the tools; another apply didn't change the measured state. When a login-shell change failed, fixing the config and rerunning completed the machine. VM boot, obtaining credentials and installing mise weren't in that timing. Windows, Ubuntu and arm64 execution still need validation.

That result justifies delegating installation, **not** assuming mise has the same sync contract. Its structured plan missed an edited SSH config; `mise dot diff` showed it; `bootstrap --yes` overwrote it. An item removed from config can remain installed. Those differences affect the CLI and the migration below.

## The operator's interface

| Situation | Proposed experience |
| --- | --- |
| Fresh native Linux machine | Run the released `install.sh` with the config URL, `--machine NAME` and optionally `--yes`. It installs maisys and calls `sync`. A VM launcher supplies those same inputs through cloud-init. |
| Fresh Windows host | Run the released `install.ps1` as the intended Windows user with the URL and machine name. It installs maisys and calls `sync`. Only system-level steps elevate. |
| Rebuilt VM with a bootstrap image | `maisys sync CONFIG-URL --machine NAME --yes`; the image needs the bootstrap binary, not a copy of the user's config. Bake WSL prerequisites into frequent-rebuild Windows images to avoid the feature-install reboot. |
| Already configured | `maisys sync` reuses the saved URL and machine. On Windows this maintains the host and finishes **new or interrupted** guests. Run `maisys sync` **inside** a completed guest to maintain its Linux environment. `maisys status` says which is which. |

For example, from a release installer on a fresh machine, or with maisys already present in the image:

```bash
# Native Ubuntu: first install (run as the workstation user, not root)
bash install.sh https://github.com/plombardi89/maisys-config.git --machine phil-workp14s --yes

# Equivalent VM image entry point when maisys is already installed
maisys sync https://github.com/plombardi89/maisys-config.git --machine phil-workp14s --yes
```

```powershell
# Proposed Windows first install, from the user's normal PowerShell session
.\install.ps1 https://github.com/plombardi89/maisys-config.git --machine workbox-camwin --yes

# Once the bootstrap reports ready
wsl -d fedora-dev
```

On a stock Windows image a reboot may be necessary. Exit **3** means “reboot, then run `maisys sync` again”; save the pending operation first. A VM launcher can do the reboot and retry. A workstation doesn't restart or close running distros behind the user's back.

## One config repo, many machines

The inventory answers **which machine, which environments, and which guest belongs to which host**. Mise answers **what is installed**. Both are ordinary files in the user's private Git repo; no private URL or machine list is compiled into the binary.

```text
maisys.toml                       # config_version = 2 (proposed)
machines/phil-workp14s.toml       # native Ubuntu laptop
machines/workbox-camwin.toml      # Windows host
machines/workbox-camwin-fc44.toml # Fedora guest; a machine in its own right
config.toml                       # global mise config; platform-neutral only
config.linux.toml                 # shared shells, Git checkouts, dotfiles
config.ubuntu.toml                # apt packages
config.fedora44.toml              # DNF packages
config.dev.toml                   # shared tool versions
config.wsl.toml                   # guest-only user settings
config.windows.toml               # WinGet apps
config.workbox-camwin-fc44.toml   # optional machine-specific override
mise.dev.lock                     # generated tool lock; commit sidecars too
files/                            # sources referred to by mise
```

Example inventory files (each block is a separate file):

```toml
# machines/phil-workp14s.toml
os = "ubuntu"
os_version = "26.04"
arch = "amd64"
environments = ["linux", "ubuntu", "dev"]
```

```toml
# machines/workbox-camwin.toml
os = "windows"
environments = ["windows"]

[wsl_host]
default_distro = "fedora-dev"

[wsl_host.wslconfig]
memory = "16GB"
networking_mode = "mirrored"

[wsl_host.distros.fedora-dev]
image = "FedoraLinux-44"
machine = "workbox-camwin-fc44"
user = "phil"
```

```toml
# machines/workbox-camwin-fc44.toml
os = "fedora"
os_version = "44"
arch = "amd64"
environments = ["linux", "fedora44", "dev", "wsl", "workbox-camwin-fc44"]
```

```toml
# config.windows.toml — a Windows app change is just a config commit
[bootstrap.packages]
"winget:Git.Git" = "latest"
"winget:Microsoft.WindowsTerminal" = "latest"
"winget:Microsoft.PowerToys" = "latest"
```

**Selection and precedence.** Explicit `--machine` wins, then the saved selection, then an exact hostname match compatible with the live system. Otherwise show a picker in a terminal or fail with the matching names in unattended use. A launcher should always supply the ID: clones can share hostnames, and a WSL guest need not have the hostname of its inventory entry. The global mise config always loads. The selected `config.<env>.toml` files load in inventory order under [mise's own merge rules](https://mise.jdx.dev/bootstrap/modules.html#combine-modules); put a machine override last. For example, a Go pin in the guest override takes precedence over the shared `dev` pin without changing the laptop. Hooks can accumulate: do not use a later hook to “override” an earlier one. There is no second maisys tool/package schema. In particular, move the spike's Linux-only base out of `config.toml` before sharing the repo with Windows.

**Validation and local state.** `maisys config validate --all` checks the inventory and references offline: valid machine IDs, existing environments, valid guest username, default distro in the host's list, guest image matching its OS/release and architecture. On sync, compare the selected machine to live facts **before applying any profile**. Config-repo CI also loads each environment combination with the supported mise version and checks the committed locks on the applicable platforms. For example, a Windows-only selection must not resolve the Linux shell configuration. Pass the exact inventory environment list to mise; do not inherit an active project's `MISE_ENV`. Reserve host-owned targets (`.wslconfig`, Terminal's default profile, guest bootstrap credentials and `/etc/wsl.conf`) for maisys; don't also declare them as mise files or hooks.

Each machine, including each guest, has its own global mise config checkout. The inventory is authoritative; its selected environments are persisted locally in gitignored `miserc.local.toml` so shells and direct mise commands see the same tools later. The URL, selected machine, last applied revision and pending bootstrap belong in local maisys settings/state, not Git. Refuse to take over an unrelated global mise config or checkout silently. Changing machines/repos gets an explicit preview; persist the new selection on apply, not on dry-run.

## Native Linux: from image to first login

1. **Start as the target user.** The installer verifies the platform-specific maisys release. For disposable VMs, cloud-init creates/selects the normal user and invokes the same installer or preinstalled binary as that user. The launcher supplies repo, machine and read-only Git access. An interactive machine can use its existing SSH key or HTTPS credential helper. `--yes` approves the plan; it does not supply Git credentials or a sudo password.
2. **Prepare the checkout and engine.** Bootstrap Git if missing; install a checksum-verified mise version pinned by the maisys release for this OS/architecture. Fetch the config once, fast-forward only, and validate the chosen commit and inventory before making it active. Keep an existing dirty checkout intact; `--local` explicitly uses it. Don't use `mise bootstrap --adopt --yes` as the discovery step on a populated machine: it can apply before the operator sees the contents.
3. **Preview and apply the same revision.** From outside any project, use only the selected machine environments, the controlled mise executable and `MISE_LOCKED=1`. For the laptop this is effectively `mise -E linux,ubuntu,dev bootstrap`. Preview with `bootstrap --dry-run` **and** `dot diff`; then call bootstrap without a second pull or version resolution. Use the user's sudo session for privileged Linux changes. An unattended image must already support the needed noninteractive sudo; fail preflight rather than hang. Keep machine-specific hooks short and safe to rerun (the Fedora login-shell and RPM Fusion hooks are examples).
4. **Verify readiness.** Check `mise bootstrap status --missing` and required managers/tools; a skipped platform manager isn't proof a package installed. Check a fresh login finds the expected toolchain, then persist success for that commit. If apply fails, report the failed phase and revision: completed steps remain, and the same command can retry. No general rollback is promised.

## Windows host → ready WSL guest

| Phase | Owner and behavior |
| --- | --- |
| Windows prerequisites | Maisys checks Windows 11 amd64, virtualization and WinGet. Ensure Git for Windows (including GCM) **before cloning a private config**, and verified native Windows mise. Git is the special first-clone prerequisite; its WinGet declaration subsequently maintains it. Sign in as the intended Windows user. |
| Windows apps | Run **Windows** mise with the `windows` environment. Its [WinGet manager](https://mise.jdx.dev/bootstrap/packages/winget.html) installs missing apps using exact package IDs. The installer of an individual app can still request UAC. Do not promise zero UAC dialogs or run the entire sync under an administrator's identity. |
| WSL and host settings | An elevated helper enables/updates WSL and required Windows features. Record a pending reboot if required. As the original user, apply `.wslconfig` and register distros: registration, per-user files and Terminal profiles belong to that user, even if different credentials were entered at UAC. Set the default distro and edit only Terminal's `defaultProfile` once the distro/profile exists; preserve other settings and comments. |
| First creation | Validate the image from the online list for a **new** distro. Record intent before `wsl --install <image> --name <name> --no-launch`. Inside the new guest as root, ensure sudo, create the specified UID 1000 user with a random password, and configure `/etc/wsl.conf` for systemd and the default user. Restart only this newly created distro when needed. Never unregister/recreate a distro to reconcile an image change. |
| Guest handoff | Install the matching verified Linux bootstrap, connect **Linux** Git to [Windows GCM](https://learn.microsoft.com/en-us/windows/wsl/tutorials/wsl-git) using the installed helper's absolute path, and use an HTTPS config URL (a guest override if the host uses SSH). Do not copy tokens or private keys. Reuse the host's **validated config commit** for the first guest apply; if it is unavailable in the guest repo, stop rather than take a different revision. Invoke Linux sync as `phil` in their Linux home. Selected `linux,fedora44,dev,wsl,…` environments and locks follow from the guest inventory; no guest questions. |
| Completion | Verify the guest's default login user, systemd where requested, and a representative tool through its login environment. Revoke temporary first-sync sudo access on success or failure; do not mark the guest ready until cleanup succeeds. Show the generated user password once to the operator (never in logs or state), or provide the host-root password-reset instructions for a noninteractive launcher. |

**Crash recovery is part of the interface.** Under the host sync lock, checkpoint `intent → registered → user ready → guest applied → ready` per distro, with the originating Windows user, target machine, image, config revision and registered distro identity once known. On retry inspect the real system before repeating a step. An existing, unrecorded distro is *pre-existing*: report “maintain from inside”, with no automatic adoption. A matching, recorded partial creation is *pending*: resume it. If a crash made ownership ambiguous between intent and registration, stop for reconciliation rather than taking over a same-named distro. A failed guest should not prevent setting up another one. Keep temporary sudo access scoped to that new guest/user and the bootstrap lifetime, with guest-side expiry/cleanup if the host disappears; verify it is gone on retry, including after a forced stop. Permanent passwordless sudo is a separate explicit policy.

After the guest is ready, Windows sync checks host settings and reports it; it does not start, terminate or resync it. Removed distros are reported as orphans. Changes requiring `wsl --shutdown` are instructions for the user, not a surprise interruption to existing sessions.

## Routine sync and deliberate differences from v1

- **One source of updates.** Maisys pulls the config once; mise applies the resulting declarations. Change tool pins and regenerate the applicable environment locks (including Python sidecars) in the config repo, review the diff, then sync. Pinned tools do not float with the release channel. Normal sync ensures OS/WinGet packages are present; use `mise bootstrap packages upgrade` for an explicit upgrade. Mise's `--update` is not a substitute for that operation.
- **Review existing machines; automate fresh ones.** Show file diffs on migration; never infer that machine-wide state is clean from `mise bootstrap plan --detailed-exitcode` alone. Proposed `maisys sync --dry-run`: `2` for known pending changes, `0` only when the checked resources agree, `1` if a required check failed or a change cannot be determined. Label opaque hooks as such; an unverified hook cannot certify a no-op. `--yes` authorizes applying the displayed config, including replacement of an edited managed copy. Don't default it on for existing workstations.
- **Explicit cleanup.** Removing a declaration stops managing it; it does not generally remove a file, uninstall a package or prune old tool versions. Use mise's explicit removal/prune operations after review. Keep a single owner per path during cutover. Don't pass old maisys state through the v1 dropped-item remover just because a resource moved to mise.

## Rollout and review gates

Keep v1 configs on their existing path during the pilot. Use a new `config_version = 2` marker so older binaries refuse the new inventory. Migrate one machine at a time: translate its profiles to native mise, compare the effective selections, back up adopted/drifted files and explicitly retire old ownership records before enabling the new path. Keep the existing Ansible route available until a populated workstation has passed a reviewed sync. A small Linux exception is cheaper than a second full provisioning engine; add one only when a specific requirement fails the pilot.

| Gate | Evidence required before expanding |
| --- | --- |
| Native Linux | Repeat clean and populated Fedora and Ubuntu installs; execute on Linux arm64 as well as amd64. Verify host/profile mismatch refusal, unattended private clone, lock replay, all configured shells after reboot, repeat apply, hand edits and one tool/package change. |
| Frequent VM rebuilds | Measure from **launcher invocation to usable login**, including Git auth, mise download and reboot where applicable. Record cold and warm runs, prompt count and second-run changes separately. Target zero guest prompts with prepared credentials; don't extrapolate a Windows timing from the Fedora result. |
| Windows + WSL | Test clean Windows 11 and an already WSL-enabled base image. Check reboot/exit 3 and rerun, actual UAC/user ownership, WinGet install and upgrade, GCM private clone, named Fedora and Ubuntu guests, login user and first tool. Interrupt after registration and during guest apply; verify resume, privilege cleanup and that a separate existing distro/session is untouched. |

**Review decision:** should mise own normal package/tool/file behavior, including the explicit upgrade/removal semantics, in exchange for keeping maisys small? If yes, revise [#1](https://github.com/plombardi89/maisys2/issues/1) before changing milestones: re-scope the overlapping [M5 modules](https://github.com/plombardi89/maisys2/pull/15), reuse the [M6 fixtures](https://github.com/plombardi89/maisys2/pull/16) to test these outcomes, and focus [M9](https://github.com/plombardi89/maisys2/pull/17) on the host/guest lifecycle rather than a second WinGet implementation. The next engineering spike is the Windows handoff on a real VM; none of those PRs is superseded by this document alone.
