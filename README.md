# maisys

maisys sets up and maintains development machines from a config repo that you keep in git. It runs on Fedora and Ubuntu, and on Windows 11 it installs WSL and sets up WSL distros.

> [!NOTE]
> maisys is in early development and doesn't do anything useful yet. The design is in [#1](https://github.com/plombardi89/maisys2/issues/1).

## Installing

On Linux, once the first release is published:

```sh
curl -fsSL https://github.com/plombardi89/maisys2/releases/latest/download/install.sh | bash -s -- CONFIG-URL
```

This installs `maisys` to `~/.local/bin` and runs `maisys sync CONFIG-URL`. Leave out `CONFIG-URL` to install without syncing. Any other arguments are passed to `maisys sync`.

To remove maisys itself (the binary, settings, state and caches), run the same command with `--uninstall` in place of `CONFIG-URL`. Tools, files and packages that maisys installed are left in place.

## Building

You need Go 1.27 or newer.

```sh
make build     # builds bin/maisys
make test      # runs the unit tests
make lint      # runs golangci-lint
make snapshot  # builds the release archives into dist/ without publishing
```

Run `make help` to list every target.

## License

[MIT](LICENSE)
