# maisys

maisys sets up and maintains development machines from a config repo that you keep in git. It runs on Fedora and Ubuntu, and on Windows 11 it installs WSL and sets up WSL distros.

> [!NOTE]
> maisys is in early development and doesn't do anything useful yet. The design is in [#1](https://github.com/plombardi89/maisys2/issues/1).

## Building

You need Go 1.27 or newer.

```sh
make build   # builds bin/maisys
make test    # runs the unit tests
make lint    # runs golangci-lint
```

Run `make help` to list every target.

## License

[MIT](LICENSE)
