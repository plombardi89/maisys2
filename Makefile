VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Lint and format tools. Each is installed into bin/ once per version, so
# local runs and CI use exactly the same versions.
GOLANGCI_LINT_VERSION ?= v2.15.0
SHFMT_VERSION         ?= v3.14.1
ACTIONLINT_VERSION    ?= v1.7.12
# shellcheck isn't a Go tool, so its release binary is downloaded and checked
# against these sha256 sums (from GitHub's release asset digests). Update the
# version and the sums together.
SHELLCHECK_VERSION        := v0.11.0
SHELLCHECK_SHA256_x86_64  := b7af85e41cc99489dcc21d66c6d5f3685138f06d34651e6d34b42ec6d54fe6f6
SHELLCHECK_SHA256_aarch64 := 68a8133197a50beb8803f8d42f9908d1af1c5540d4bb05fdfca8c1fa47decefc

GOLANGCI_LINT := bin/golangci-lint-$(GOLANGCI_LINT_VERSION)
SHFMT         := bin/shfmt-$(SHFMT_VERSION)
ACTIONLINT    := bin/actionlint-$(ACTIONLINT_VERSION)
SHELLCHECK    := bin/shellcheck-$(SHELLCHECK_VERSION)

# Every shell script git tracks, found by extension or #! line.
SHELL_SCRIPTS = $$(git ls-files -z | xargs -0 $(SHFMT) -f)

GORELEASER_VERSION ?= v2.20.0
GORELEASER := go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

BUILDINFO := github.com/plombardi89/maisys2/internal/buildinfo
LDFLAGS   := -s -w -X $(BUILDINFO).version=$(VERSION) -X $(BUILDINFO).commit=$(COMMIT) -X $(BUILDINFO).date=$(DATE)

.PHONY: help build test e2e upgrade-test signin-test fixture lint lint-go lint-shell lint-workflows fmt generate tidy snapshot clean

help: ## Show this help
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

build: ## Build bin/maisys
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/maisys ./cmd/maisys

test: ## Run the unit tests
	go test ./...

# The distro make e2e tests, such as fedora-44, or all of them (test/e2e).
DISTRO ?= all

e2e: ## Run the end-to-end tests in podman containers: every distro, or DISTRO=fedora-44
	MAISYS_E2E='$(DISTRO)' go test -count=1 -timeout 3h -v -run '^TestSync$$' ./test/e2e

# The upgrade test's releases (test/e2e): DRAFT is a folder of the draft
# release's files, such as dist/ from make snapshot, and PREVIOUS the URL of
# the previous release's files or a maisys binary (by default the latest
# published release).
DRAFT    ?=
PREVIOUS ?=

upgrade-test: ## Run the upgrade test: PREVIOUS updates itself to DRAFT=folder (on DISTRO, as for e2e)
	@[ -n '$(DRAFT)' ] || { echo 'usage: make upgrade-test DRAFT=folder [PREVIOUS=release-url-or-binary] [DISTRO=fedora-44]' >&2; exit 2; }
	MAISYS_E2E='$(DISTRO)' MAISYS_UPGRADE_DRAFT='$(abspath $(DRAFT))' \
	MAISYS_UPGRADE_PREVIOUS='$(if $(findstring ://,$(PREVIOUS)),$(PREVIOUS),$(abspath $(PREVIOUS)))' \
	go test -count=1 -timeout 3h -v -run '^TestUpgrade$$' ./test/e2e

signin-test: ## Run the private config repo sign-in test (needs MAISYS_FIXTURE_URL and MAISYS_FIXTURE_TOKEN)
	go test -count=1 -timeout 30m -v -run '^TestSignIn$$' ./test/signin

fixture: ## Build the end-to-end tests' fixture config repo in DIR
	@[ -n '$(DIR)' ] || { echo 'usage: make fixture DIR=folder' >&2; exit 2; }
	go run ./test/cmd/fixture '$(DIR)'

lint: lint-go lint-shell lint-workflows ## Run every linter

lint-go: $(GOLANGCI_LINT) ## Lint Go code (Linux and Windows builds), including formatting
	$(GOLANGCI_LINT) run
	GOOS=windows $(GOLANGCI_LINT) run

lint-shell: $(SHELLCHECK) $(SHFMT) ## Lint shell scripts with shellcheck, and check their formatting with shfmt
	$(SHELLCHECK) $(SHELL_SCRIPTS)
	$(SHFMT) -d $(SHELL_SCRIPTS)

lint-workflows: $(ACTIONLINT) $(SHELLCHECK) ## Lint GitHub workflows with actionlint, including shellcheck on run: steps
	$(ACTIONLINT) -shellcheck $(SHELLCHECK)

fmt: $(GOLANGCI_LINT) $(SHFMT) ## Format Go code (gofumpt, goimports) and shell scripts (shfmt)
	$(GOLANGCI_LINT) fmt
	$(SHFMT) -w $(SHELL_SCRIPTS)

$(GOLANGCI_LINT):
	GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv bin/golangci-lint $@

$(SHFMT):
	GOBIN=$(CURDIR)/bin go install mvdan.cc/sh/v3/cmd/shfmt@$(SHFMT_VERSION)
	mv bin/shfmt $@

$(ACTIONLINT):
	GOBIN=$(CURDIR)/bin go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	mv bin/actionlint $@

$(SHELLCHECK):
	@set -eu; \
	[ "$$(uname -s)" = Linux ] || { echo "no pinned shellcheck for $$(uname -s); install shellcheck $(SHELLCHECK_VERSION) as $@" >&2; exit 1; }; \
	case "$$(uname -m)" in \
	x86_64 | amd64) arch=x86_64; sum=$(SHELLCHECK_SHA256_x86_64) ;; \
	aarch64 | arm64) arch=aarch64; sum=$(SHELLCHECK_SHA256_aarch64) ;; \
	*) echo "no pinned shellcheck for $$(uname -m)" >&2; exit 1 ;; \
	esac; \
	archive=shellcheck-$(SHELLCHECK_VERSION).linux.$$arch.tar.gz; \
	rm -rf bin/shellcheck.tmp; \
	trap 'rm -rf bin/shellcheck.tmp' EXIT; \
	mkdir -p bin/shellcheck.tmp; \
	echo "downloading $$archive"; \
	curl -fsSL -o bin/shellcheck.tmp/$$archive https://github.com/koalaman/shellcheck/releases/download/$(SHELLCHECK_VERSION)/$$archive; \
	echo "$$sum  bin/shellcheck.tmp/$$archive" | sha256sum -c --quiet -; \
	tar -xzf bin/shellcheck.tmp/$$archive -C bin/shellcheck.tmp; \
	mv bin/shellcheck.tmp/shellcheck-$(SHELLCHECK_VERSION)/shellcheck $@

generate: ## Regenerate generated files
	go generate ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

snapshot: ## Build release archives into dist/ without publishing
	$(GORELEASER) release --snapshot --clean

clean: ## Remove build output and installed tools
	rm -rf bin dist
