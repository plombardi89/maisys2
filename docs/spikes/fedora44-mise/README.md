# Can mise provision our Fedora workstations?

**Decision: use mise for the next Fedora pilot and pause the general-purpose Linux modules in M5.** Let the private config repo own tool versions, packages, shell setup and dotfiles. Keep maisys2 available for Windows-to-WSL provisioning and for any specific maintenance guarantee we decide is essential. Don't build a translator between two config formats or have two programs own the same file.

The strongest evidence for switching is the ordinary second run: after a real reboot, tools worked in all three shells, and applying the same config again didn't change the measured workstation state. The strongest argument for keeping part of maisys is **file ownership**: mise can report a clean plan while a hand-edited file is about to be overwritten. That matters more to a long-lived personal machine than how quickly a new one installs.

## What ran

On 2026-11-20 we booted disposable QEMU/KVM guests from Fedora Cloud Base Generic **44-1.7 x86_64** (kernel `6.19.10-300.fc44.x86_64`). The Fedora-published [image SHA-256](https://fedoraproject.org/releases.json) was `28680fe5b371a5a82ebf43a31926e086a168e59949d03969c5093e7071f90b7f`. We used [mise 2026.11.10](https://github.com/jdx/mise/releases/tag/v2026.11.10) and the image's `fedora` account: UID 1000, SSH key login, passwordless sudo. The config origin was a bare Git repo served by SSH inside the guest, using a disposable key. This exercised private Git access without relying on a hosted service or a host credential.

The final locked config bootstrapped a third, clean guest in **48 seconds** from invoking mise through installing the tools (excludes boot, downloading mise and setting up the SSH key). No Git or developer tools were preinstalled by the test. It passed `mise lock --global --platform linux-x64,linux-arm64 --check`. After reboot, all three login shells ran Go, codespell and uv; a locked repeat installed zero tools and left the measured RPMs, files and checkouts unchanged. The timing is one observation, not a benchmark.

The [runnable config](config/) is a subset of the repo's [example profiles](../../../internal/modules/testdata/example/profiles/), organized as a global mise config with optional environments:

| File | What it owns |
| --- | --- |
| [`config.toml`](config/config.toml) | Login shell, Bash/Zsh/Fish activation, four Git checkouts (oh-my-zsh, theme and plugins), a Zsh startup block, templated Git identity, and a copied SSH client config at `0600`. |
| [`config.fedora44.toml`](config/config.fedora44.toml) | The eight packages in the maisys Fedora list and four prerequisites. Use real RPM names: `util-linux` owns `chsh`; declaring the capability `util-linux-user` made mise propose installing it again even though DNF considered it satisfied. |
| [`config.dev.toml`](config/config.dev.toml) | Five pinned tools: Go, golangci-lint, kubectl, uv and the isolated Python CLI codespell. The [tool lock](config/mise.dev.lock) and [Python dependency lock](config/locks/mise.dev/pypi-codespell/2.4.1/uv.lock) travel with the repo. |
| [`config.unattended.toml`](config/config.unattended.toml) | A guarded `sudo -n chsh` for the key-only account. The regular login-shell declaration still reports the desired state. |
| [`config.rpmfusion.toml`](config/config.rpmfusion.toml) | An optional hook installs the signed free/nonfree release RPMs before the packages phase, using keys shipped by Fedora. `unrar` is the small test package. |

## Findings from the guests

| Experiment | Observation | Consequence |
| --- | --- | --- |
| Adopt a private config on a fresh machine | With no checkout, `--adopt … --dry-run` printed the clone command only. Real adoption installed missing Git on a clean image, cloned over SSH and saved the selected environments in `miserc.local.toml`. | For a full **first-run** review on an existing machine, install Git, clone the config first, then preview it. An SSH key and known host are still prerequisites for this auth path. |
| Fail midway, fix in Git, rerun | Stock `chsh` asked for a password and failed. RPMs, four repos and dotfiles had already been applied; the account still used Bash and all five tools were missing. After a fast-forward to the guarded hook, the rerun skipped those completed resources, switched the shell and installed the tools. | Recovery worked without reinstalling or cleaning up. It is not rollback: a failure can leave a partly configured machine. `--yes` does not answer PAM or sudo. |
| Reboot, log in, apply again | Fresh interactive Bash, Zsh and Fish each ran `go version` (`go1.26.4 linux/amd64`) and `codespell --version` (`2.4.1`). Zsh loaded both plugin functions and selected Aphrodite. Git returned the templated email; SSH config was `0600`. The next apply took 0.49 s: 12 RPMs already present, 4 repos current, 0 tools installed. | Before reboot and after the repeat, RPM versions/install times, dotfile contents/modes/mtimes/inodes, checkout commits, login shell and tool install directory timestamps matched. The hook still ran its read-only guard. |
| Bump one tool; upgrade one RPM | A config commit changed kubectl `1.35.0` → `1.35.1`; `bootstrap --update` pulled it, installed one tool and skipped four. Both kubectl versions stayed on disk. Netcat stayed `1.237-3.fc44` until `bootstrap packages upgrade dnf:netcat`, which installed `1.238-1.fc44` in a one-package transaction. | `--update` is not a request to upgrade all installed packages. Keep tool bumps reviewable in Git and make OS upgrades an explicit operation. Dropped tool versions need a separate pruning policy. |
| Edit the managed SSH config | `bootstrap plan --detailed-exitcode` said **0 changes / exit 0**. `bootstrap --dry-run` marked the copy as edited; `bootstrap status --missing` exited **1**; `mise dot diff` showed the exact line. Without a TTY, ordinary `bootstrap` exited **1** and preserved the edit. `bootstrap --yes` warned, exited **0**, and **overwrote it**. No checkpoint for that path appeared in `mise dot history`. A first-time inline declaration also replaced an existing file with `--yes`. | Do not use the plan exit code as an all-resource drift gate. Review the dotfile diff before applying; choose deliberately which files should be authoritative copies. A copy declaration on its own is not a backup. |
| Remove declarations | We edited the SSH file again, deleted its declaration and `dnf:htop` in Git, then synced. The SSH file retained the edit (SHA-256 unchanged), htop remained installed, and status was clean. | Clean means *current declarations match*. Unlike the [maisys design](https://github.com/plombardi89/maisys2/issues/1), deletion from config is not a journal-driven cleanup. Use explicit removal when needed. |
| Add RPM Fusion | A clean guest installed `rpmfusion-{free,nonfree}-release-44-3`, with local package signature checking enabled, then `unrar-7.2.7-1.fc44`. DNF found Steam in the nonfree repos. The next bootstrap saw 13 installed RPM declarations; the repo hook did not rerun its install transaction. | This Fedora-specific case needed one guarded hook, not a package-manager implementation. Dry-run prints hook code; it cannot tell you the changes inside it. Steam installation and graphical launch were not tested. |
| Lock tools | With uv `0.11.6`, `mise lock` returned 0 **with a warning**, but `mise lock --check` failed because codespell had no dependency graph. With uv `0.12.10` the graph was generated and the check passed for 5 tools across 2 platforms. | Run the lock **check**, not just generation, in config-repo CI. Go and kubectl had checksums; uv and golangci-lint also had GitHub attestations; codespell used a hashed Python dependency lock. Generating arm64 metadata is not an arm64 installation test. |

## What the operator would do

**Reproduce on a disposable Fedora 44 x86_64 guest.** Publish the *contents* of [`config/`](config/) at the root of a Git repo the guest can read, including the locks. The examples below assume SSH access to that repo is already working. The test binary was installed on the system PATH, so all three login shells could find it before activation:

```bash
curl -fL -o mise-bin https://github.com/jdx/mise/releases/download/v2026.11.10/mise-v2026.11.10-linux-x64
echo 'b603a4f48563fc379a794d9ea2cf9f709ed276c9141068604cb0c59fa3e674a9  mise-bin' | sha256sum -c - && sudo install -m 0755 mise-bin /usr/local/bin/mise

# Fresh key-only VM: select environments once. Omit rpmfusion if unwanted.
MISE_LOCKED=1 mise -E fedora44,dev,unattended,rpmfusion bootstrap --adopt "$CONFIG_URL" --save-env --yes
```

**On a workstation with files you care about**, review the incoming config and the resulting dotfile changes separately. Run from your home directory so a project config doesn't contribute packages, tasks or hooks. If you cloned manually, first select the profiles by putting `env = ["fedora44", "dev"]` in `~/.config/mise/miserc.local.toml` (adoption with `--save-env` already did this). Once `~/.config/mise` is the config checkout:

```bash
cd ~
git -C ~/.config/mise pull --ff-only
mise bootstrap --dry-run
mise dot diff
MISE_LOCKED=1 mise bootstrap        # interactive apply
mise bootstrap status --missing     # check the resulting state
```

For an explicit package upgrade, preview `mise bootstrap packages upgrade --dry-run`, then run `mise bootstrap packages upgrade`. To change a tool pin, change it in the appropriate environment file in the config repo, regenerate with `mise lock --global --platform linux-x64,linux-arm64` in the adopted, active config, run the same command with `--check`, and review the lock diff before publishing. The lock covers tools, not DNF metadata or the tips of the theme/plugin branches.

## Alternatives and next decision

| Option | When it earns its place |
| --- | --- |
| **mise alone** | Best starting point for this fleet. The sample needs two optional hooks: one for a key-only Fedora account and one for RPM Fusion. Neither reimplements tool installation. Use native mise files for the paths where the config repo *should* win; use a line/block edit when only part of a shared file belongs to the repo. |
| **mise + [chezmoi](https://www.chezmoi.io/what-does-chezmoi-do/)** | Worth considering if managing hand edits becomes a job in itself. We ran Fedora's chezmoi `2.73.0` as a separate user. After both the source and the destination changed, `chezmoi diff` showed both versions; ordinary noninteractive `chezmoi apply` tried to ask, exited 1 without a TTY, and **left the hand edit intact**. Ordinary mise also refused the untracked overwrite without `--yes`, so that safeguard alone doesn't justify adding chezmoi. Choose it for its dedicated diff/merge workflow, and give it sole ownership of those paths. |
| **mise + a small maisys helper** | Keep custom code for a real boundary: Windows elevation/reboot, creating a distro once, handing off Git credentials and running the first guest bootstrap. Or retain a narrowly scoped Linux helper if preserving adopted files on removal is mandatory. The Fedora runs do not justify retaining the generic tool downloaders or duplicating the mise catalog. |
| **mise + [Ansible](https://docs.ansible.com/projects/ansible/latest/playbook_guide/playbooks_checkmode.html)** | A reasonable fallback if privileged setup grows into many ordered hooks. DNF5 has a [native module](https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/dnf5_module.html); supported modules can report changes in check/diff mode. It also brings back Python/libdnf5 and playbook maintenance, two costs the original project was avoiding. Documentation comparison only; no second provisioning stack was needed for this fixture. |

**Next step:** pilot the locked config on one noncritical, already-configured Fedora workstation. Inventory its existing dotfiles, decide which copy is authoritative for each, and confirm the preview and first interactive apply match that decision. Keep the existing Ansible setup available until that succeeds. Then port the shared CLI and package profiles rather than implementing them in M5. Validate Ubuntu, an actual arm64 machine and a real WSL guest separately before migrating those machines. No Windows host lifecycle, graphical desktop, sudoers removal, legacy cleanup, or whole-fleet cutover was exercised here.

Further reading: mise's [bootstrap phases and plan coverage](https://mise.jdx.dev/bootstrap.html), [file conflict rules](https://mise.jdx.dev/dotfiles/managed.html#conflicts), [module removal rules](https://mise.jdx.dev/bootstrap/modules.html#remove-a-module-s-resources), and [lockfile guarantees by backend](https://mise.jdx.dev/dev-tools/mise-lock.html#backend-support).
