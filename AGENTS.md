# Project overview

maisys is a Go binary that sets up and maintains development machines (Fedora, Ubuntu, and Windows 11 as a WSL host) from a separate config repo. The design, decisions and milestones are in issue #1 (https://github.com/plombardi89/maisys2/issues/1). Treat it as the source of truth, and follow its milestones.

## Repository structure

- `cmd/maisys/` - the `maisys` entry point.
- `internal/buildinfo/` - version information set at build time with `-ldflags`, including the repo self-update installs from (`updateRepo`). Release builds set it from `GITHUB_REPOSITORY`, so a fork's releases update from the fork; a fork also needs to change the default URL in `install.sh`.
- `internal/cli/` - the cobra command tree (`sync`, `config`, `status`).
- `internal/config/` - loads a machine's config from a config repo: `machines/<name>.toml` and the profiles it lists, merged on top of each module's defaults, with every error reported as `file:line:column`. Module config structs declare their keys with `toml` tags; lists tagged `merge:"union"` combine across layers, tables keyed by name merge entry by entry (new entries start from `SetDefaults` if their type has it), and everything else is replaced by the later layer. The loader rejects config types it can't merge reliably (untagged fields, pointers, interfaces), and keys are case-sensitive.
- `internal/modules/` - the built-in modules, one package each (`internal/modules/<name>`), listed in order by `modules.All()`. Each package exports an empty `Module` type that implements `config.Module`: `Info()` returns its name, summary, and supported OSes and architectures, and `NewConfig()` returns its defaults in the package's `Config` struct. Field doc comments describe each key for users, and will feed the generated docs and schema. Modules are off by default, versioned modules default to `"latest"`, and `merge:"union"` lists must start empty, because later layers can't remove entries; the tests check all three. `testdata/example/` is the example config repo from issue #1; keep the two in sync.
- `internal/selfupdate/` - checks the latest release's `checksums.txt` and replaces the running binary if that release is newer (the first step of `sync`). `MAISYS_RELEASE_URL` points it, and `install.sh`, at other release files (http, https or file) for testing.
- `.github/workflows/` - CI (`ci.yml`) and releases (`release.yml`).
- `.github/actionlint.yaml` - extra runner labels for actionlint. Add new GitHub-hosted labels here when actionlint doesn't know them yet.
- `.golangci.yml` - Go linters and formatters (gofumpt, goimports).
- `.goreleaser.yaml` - how release archives and checksums are built.
- `install.sh` - the Linux installer and uninstaller, published with each release. It must stay a single self-contained bash script.
- `test/install/` - Go tests for `install.sh`. They pipe it into bash against a fake release, with only the tools it's allowed to use on PATH. If `install.sh` needs a new command, add it to `baseTools` there.

New packages go under `internal/`, following the layout in issue #1.

## Development

- `make build`, `make test`, `make lint` and `make fmt`. Run `make help` for the full list.
- Go version: see `go.mod`. If your installed Go is older, `go` downloads the right version automatically.
- `make lint` runs three linters. Fix whatever they report rather than disabling the check; if something really is intended, use a `//nolint:<linter> // reason` comment (Go) or a `# shellcheck disable=SCxxxx # reason` comment (shell).
    - `make lint-go`: golangci-lint on both the Linux and the Windows build (`GOOS=windows`), so Windows-only files are checked too. It also fails on Go code that isn't formatted with gofumpt and goimports.
    - `make lint-shell`: shellcheck and shfmt on every shell script git tracks, found by file extension or `#!` line, so new scripts are covered automatically.
    - `make lint-workflows`: actionlint on the GitHub workflows, including shellcheck on their `run:` steps.
- `make fmt` formats Go (gofumpt, goimports) and shell scripts (shfmt).
- The lint tools' versions are pinned at the top of the `Makefile` and installed into `bin/` on first use, so local runs and CI use the same versions. The golangci-lint version is also set in `ci.yml` for golangci-lint's GitHub Action; change both together.
- Keep `go.mod` tidy (`make tidy`). CI fails if `go mod tidy` would change anything.
- Tests use the standard `testing` package and are table-driven where it helps.
- CI runs the tests on Ubuntu 24.04 and 26.04, Ubuntu 26.04 on arm64, Fedora 43, 44 and 45 (in containers), and Windows, so code must build and pass on all of them. The list follows the rolling support policy in issue #1, and the arm64 entry follows the newest Ubuntu LTS: when a release reaches end of life or a new one ships, update the matrix in `ci.yml` and the required checks in the `main` ruleset together.
- `make snapshot` builds the release archives into `dist/` without publishing anything. CI does the same on every pull request.

## Releases

Pushing a `v*` tag runs `release.yml`: CI, then GoReleaser creates a **draft** release, then the draft is published. Jobs that test the draft's files are added between those steps as they're built. Self-update follows `releases/latest`, which never includes drafts, so a release only reaches machines once every test has passed.

## Conventions

- Commands that exist but don't work yet return the `notImplemented` error, so the command line keeps the shape described in issue #1.
- Exit codes: 0 success, 1 error, 2 changes pending (`sync --dry-run`), 3 reboot needed (Windows).
- Changes land through pull requests, and CI must pass before merging. Pull requests are squash-merged, must be up to date with `main`, and their branches are deleted after merging.

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
