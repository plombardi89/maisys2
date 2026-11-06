VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GOLANGCI_LINT_VERSION ?= v2.15.0
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

GORELEASER_VERSION ?= v2.20.0
GORELEASER := go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

BUILDINFO := github.com/plombardi89/maisys2/internal/buildinfo
LDFLAGS   := -s -w -X $(BUILDINFO).version=$(VERSION) -X $(BUILDINFO).commit=$(COMMIT) -X $(BUILDINFO).date=$(DATE)

.PHONY: help build test lint fmt generate tidy snapshot clean

help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build bin/maisys
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/maisys ./cmd/maisys

test: ## Run the unit tests
	go test ./...

lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run

fmt: ## Format the code
	$(GOLANGCI_LINT) fmt

generate: ## Regenerate generated files
	go generate ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

snapshot: ## Build release archives into dist/ without publishing
	$(GORELEASER) release --snapshot --clean

clean: ## Remove build output
	rm -rf bin dist
