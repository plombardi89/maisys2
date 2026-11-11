# maisys

maisys sets up and maintains development machines from a config repo that you keep in git. It runs on Fedora and Ubuntu, and on Windows 11 it installs WSL and sets up WSL distros.

> [!NOTE]
> maisys is in early development. It can create, check and sync a config repo, but it doesn't change your machine yet. The design is in [#1](https://github.com/plombardi89/maisys2/issues/1).

## Installing

On Linux (amd64 or arm64):

```sh
curl -fsSL https://github.com/plombardi89/maisys2/releases/latest/download/install.sh | bash -s -- CONFIG-URL
```

This installs `maisys` to `~/.local/bin` and runs `maisys sync CONFIG-URL`. Leave out `CONFIG-URL` to install without syncing. Any other arguments are passed to `maisys sync`.

To remove maisys itself (the binary, settings, state and caches), run the same command with `--uninstall` in place of `CONFIG-URL`. Tools, files and packages that maisys installed are left in place.

## Your config repo

maisys reads everything it does from a config repo in git: a `machines/` file for each machine, and `profiles/` that machines share. To start one, run:

```sh
maisys config init
```

This creates `~/maisys-config` with a machine file for this machine and every module listed, commented out, in `profiles/base.toml`. Then:

- `maisys config` shows this machine's merged config, and `maisys config --explain` shows which file set each value.
- `maisys config validate` checks it, and `maisys config validate --all` checks every machine (the repo's own CI runs this). They also report keys maisys doesn't know, such as typos, which other commands ignore so that a config written for a newer maisys still works.
- `maisys sync CONFIG-URL` sets up another machine from the same repo once it's pushed somewhere git can reach. For a private repo over HTTPS, maisys uses git's own sign-in, asks for a read-only token if git can't sign in, or uses `MAISYS_CONFIG_TOKEN`.

The modules and their settings are described in [docs/modules.md](docs/modules.md). Editors that understand JSON schemas for TOML can complete and check config files with `config.schema.json`, which is published with each release.

## Updating

maisys keeps itself up to date. Every `maisys sync` first checks for a newer release, and if there is one, installs it and carries on with the new version. It never moves to an older release. `maisys sync --local` skips the check, and if the check fails (for example, with no network), maisys warns and carries on with the version you have.

## Building

You need Go 1.27 or newer.

```sh
make build     # builds bin/maisys
make test      # runs the unit tests
make lint      # lints Go code, shell scripts and GitHub workflows
make fmt       # formats Go code (gofumpt, goimports) and shell scripts (shfmt)
make generate  # regenerates docs/modules.md and the config schema from the module configs
make snapshot  # builds the release archives into dist/ without publishing
```

Run `make help` to list every target. The lint tools are pinned in the `Makefile` and installed into `bin/` the first time you run `make lint`.

CI fails on Go code that isn't formatted with gofumpt. To have your editor format the same way when you save, turn on gopls's gofumpt setting (`"formatting.gofumpt": true`). Imports are grouped by `make fmt`.

## License

[MIT](LICENSE)
