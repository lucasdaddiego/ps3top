# ps3top — htop for a HEN'd PS3 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := ps3top
INSTALL_DIR := $(HOME)/.bin
# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE     := -trimpath -ldflags "-s -w"

.DEFAULT_GOAL := help
.PHONY: help build run test install clean

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the binary into ./ps3top
	go build -o $(BINARY) .

run: ## Launch the TUI (auto-discovers the console)
	go run .

test: ## Vet and run the test suite
	go vet ./...
	go test ./...

install: ## Install a stripped release binary into ~/.bin
	@mkdir -p $(INSTALL_DIR)
	go build $(RELEASE) -o "$(INSTALL_DIR)/$(BINARY)" .
	@echo "installed $(INSTALL_DIR)/$(BINARY)"

clean: ## Remove the local build artifact
	rm -f $(BINARY)
