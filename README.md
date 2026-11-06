# maisys

maisys sets up and maintains development machines from a config repo that you keep in git. It runs on Fedora and Ubuntu, and on Windows 11 it installs WSL and sets up WSL distros.

> [!NOTE]
> maisys is in early development and doesn't do anything useful yet. The design is in [#1](https://github.com/plombardi89/maisys2/issues/1).

## Installing

On Linux (amd64 or arm64), once the first release is published:

```sh
curl -fsSL https://github.com/plombardi89/maisys2/releases/latest/download/install.sh | bash -s -- CONFIG-URL
```

This installs `maisys` to `~/.local/bin` and runs `maisys sync CONFIG-URL`. Leave out `CONFIG-URL` to install without syncing. Any other arguments are passed to `maisys sync`.

To remove maisys itself (the binary, settings, state and caches), run the same command with `--uninstall` in place of `CONFIG-URL`. Tools, files and packages that maisys installed are left in place.

## Updating

maisys keeps itself up to date. Every `maisys sync` first checks for a newer release, and if there is one, installs it and carries on with the new version. It never moves to an older release. `maisys sync --local` skips the check, and if the check fails (for example, with no network), maisys warns and carries on with the version you have.

## Building

You need Go 1.27 or newer.

```sh
make build     # builds bin/maisys
make test      # runs the unit tests
make lint      # lints Go code, shell scripts and GitHub workflows
make fmt       # formats Go code (gofumpt, goimports) and shell scripts (shfmt)
make snapshot  # builds the release archives into dist/ without publishing
```

Run `make help` to list every target. The lint tools are pinned in the `Makefile` and installed into `bin/` the first time you run `make lint`.

CI fails on Go code that isn't formatted with gofumpt. To have your editor format the same way when you save, turn on gopls's gofumpt setting (`"formatting.gofumpt": true`). Imports are grouped by `make fmt`.

## License

[MIT](LICENSE)
