# ps3top — htop for a HEN'd PS3 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := ps3top
TOOL        := binmerge
BIN_DIR     := bin
INSTALL_DIR ?= $(HOME)/.bin

# Version metadata, stamped in at link time — both binaries take the same one
# (cmd/binmerge is package main too, so -X main.version reaches it unchanged),
# and binmerge keeps its own tool version alongside it in the source. A plain `go build` / `go run .`
# leaves the defaults ("dev"), which is the honest answer — the source moves on
# between tags, and a --version that names a release the code has passed sends
# bug reports chasing the wrong revision.
VERSION := $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
STAMP   := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE := -trimpath -ldflags "-s -w $(STAMP)"

.DEFAULT_GOAL := help
.PHONY: help build run test lint tidy install clean

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile both binaries into bin/ (unstripped; replaces the ~/.bin-linked commands)
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(STAMP)" -o $(BIN_DIR)/$(BINARY) .
	go build -ldflags "$(STAMP)" -o $(BIN_DIR)/$(TOOL) ./cmd/$(TOOL)

run: ## Launch the TUI (auto-discovers the console)
	go run .

test: ## Vet and run the test suite with the race detector
	go vet ./...
	go test -race ./...

lint: ## Vet, gofmt check, and staticcheck when it's installed
	go vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: files need formatting"; exit 1; }
	@command -v staticcheck >/dev/null && staticcheck ./... || echo "staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

tidy: ## Update dependencies to their latest minor/patch and tidy go.mod
	go get -u ./...
	go mod tidy

install: ## Build stripped release binaries into bin/ and link them from ~/.bin
	@mkdir -p $(BIN_DIR) "$(INSTALL_DIR)"
	go build $(RELEASE) -o $(BIN_DIR)/$(BINARY) .
	go build $(RELEASE) -o $(BIN_DIR)/$(TOOL) ./cmd/$(TOOL)
	ln -sfn "$(CURDIR)/$(BIN_DIR)/$(BINARY)" "$(INSTALL_DIR)/$(BINARY)"
	ln -sfn "$(CURDIR)/$(BIN_DIR)/$(TOOL)" "$(INSTALL_DIR)/$(TOOL)"

clean: ## Remove the local build directory (the ~/.bin links then dangle until make install)
	rm -rf $(BIN_DIR)
