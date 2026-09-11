.PHONY: build test test-podman clean install run help

# Project variables
BINARY_NAME=late
VERSION?=1.5.1

# Go compiler flags
LDFLAGS=-ldflags "-X late/internal/common.Version=${VERSION}"

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the late binary
	@echo "Building ${BINARY_NAME}..."
	@go build ${LDFLAGS} -o bin/${BINARY_NAME} ./cmd/late

test: ## Run tests for the entire project
	@echo "Running tests..."
	@go test -v -race ./...
	@./test/late-podman-test.sh

test-podman: ## Test the Podman launcher without requiring Podman
	@./test/late-podman-test.sh

clean: ## Remove build artifacts
	@echo "Cleaning..."
	@rm -rf bin/

install: build ## Build and install the binary to your Go bin path
	@echo "Installing to ~/.local/bin/late..."
	@go build ${LDFLAGS} -o bin/${BINARY_NAME} ./cmd/late
	@mv bin/${BINARY_NAME} ~/.local/bin/late
	@install -m 0755 late-podman ~/.local/bin/late-podman

run: build ## Build and run the project
	@./bin/${BINARY_NAME}
