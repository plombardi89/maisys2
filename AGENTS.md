# Project overview

maisys is a Go binary that sets up and maintains development machines (Fedora, Ubuntu, and Windows 11 as a WSL host) from a separate config repo. The design, decisions and milestones are in issue #1 (https://github.com/plombardi89/maisys2/issues/1). Treat it as the source of truth, and follow its milestones.

## Repository structure

- `cmd/maisys/` - the `maisys` entry point.
- `internal/buildinfo/` - version information set at build time with `-ldflags`.
- `internal/cli/` - the cobra command tree (`sync`, `config`, `status`).
- `.github/workflows/` - CI (`ci.yml`) and releases (`release.yml`).
- `.goreleaser.yaml` - how release archives and checksums are built.

New packages go under `internal/`, following the layout in issue #1.

## Development

- `make build`, `make test`, `make lint` and `make fmt`. Run `make help` for the full list.
- Go version: see `go.mod`. If your installed Go is older, `go` downloads the right version automatically.
- `make lint` runs golangci-lint through `go run`, so it doesn't depend on a locally installed copy.
- Keep `go.mod` tidy (`make tidy`). CI fails if `go mod tidy` would change anything.
- Tests use the standard `testing` package and are table-driven where it helps.
- CI runs tests on Linux and Windows, so code must build and pass on both.
- `make snapshot` builds the release archives into `dist/` without publishing anything. CI does the same on every pull request.

## Releases

Pushing a `v*` tag runs `release.yml`: CI, then GoReleaser creates a **draft** release, then the draft is published. Jobs that test the draft's files are added between those steps as they're built. Self-update follows `releases/latest`, which never includes drafts, so a release only reaches machines once every test has passed.

## Conventions

- Commands that exist but don't work yet return the `notImplemented` error, so the command line keeps the shape described in issue #1.
- Exit codes: 0 success, 1 error, 2 changes pending (`sync --dry-run`), 3 reboot needed (Windows).
- Changes land through pull requests, and CI must pass before merging.

## Boundaries

- **Ask first**
    - Large cross-package refactors.
    - New dependencies with broad impact.
    - Changes that depart from the design in issue #1.
- **Never**
    - Commit secrets, credentials, or tokens.
    - Edit generated files by hand when a generation workflow exists.
    - Use destructive git operations unless explicitly requested.
    - Go outside the project boundary, for example, DO NOT edit files in user's home directories, add or edit files
      in /tmp or anywhere else on the host filesystem.
