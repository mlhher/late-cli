.PHONY: build test clean install run help

# Project variables
BINARY_NAME=late
VERSION?=1.2.4

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

clean: ## Remove build artifacts
	@echo "Cleaning..."
	@rm -rf bin/

install: build ## Build and install the binary to your Go bin path
	@echo "Installing to ~/.local/bin/late..."
	@go build ${LDFLAGS} -o bin/${BINARY_NAME} ./cmd/late
	@mv bin/${BINARY_NAME} ~/.local/bin/late

run: build ## Build and run the project
	@./bin/${BINARY_NAME}
