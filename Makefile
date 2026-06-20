# lp10 — terminal player for the Arylic LP10 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := lp10
PKG         := .
INSTALL_DIR := $(HOME)/.bin
GOFLAGS     ?=
# Release build: strip the symbol table and DWARF (-s -w) and drop local file
# paths (-trimpath) for a smaller, debug-free binary.
RELEASE     := -trimpath -ldflags "-s -w"

.DEFAULT_GOAL := help

.PHONY: help build run test race cover vet fmt fmt-check tidy check deploy install uninstall keychain clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the lp10 binary
	go build $(GOFLAGS) -o $(BINARY) $(PKG)

run: ## Build and launch the live TUI (needs a terminal + Keychain item)
	go run $(PKG)

test: ## Run the test suite
	go test $(GOFLAGS) ./...

race: ## Run the test suite under the race detector
	go test -race -count=1 ./...

cover: ## Run tests with coverage and print the per-function summary
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go files
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

tidy: ## Tidy go.mod / go.sum
	go mod tidy

check: fmt-check vet test ## Run fmt-check, vet, and tests (CI gate)

deploy: ## Build a stripped, debug-free release binary into ~/.bin
	@mkdir -p $(INSTALL_DIR)
	rm -f "$(INSTALL_DIR)/$(BINARY)"
	go build $(RELEASE) -o "$(INSTALL_DIR)/$(BINARY)" $(PKG)
	@echo "deployed $(INSTALL_DIR)/$(BINARY) ($$(du -h "$(INSTALL_DIR)/$(BINARY)" | cut -f1))"

install: build ## Symlink the binary into ~/.bin
	@mkdir -p $(INSTALL_DIR)
	ln -sf "$(CURDIR)/$(BINARY)" "$(INSTALL_DIR)/$(BINARY)"
	@echo "installed $(INSTALL_DIR)/$(BINARY) -> $(CURDIR)/$(BINARY)"

uninstall: ## Remove the ~/.bin symlink
	rm -f "$(INSTALL_DIR)/$(BINARY)"

keychain: ## One-time: store the LP10 root password in the Keychain
	security add-generic-password -U -a root -s lp10 -w

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	go clean
