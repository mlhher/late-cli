.PHONY: build test test-podman vet lint vulncheck check clean install run help

# Project variables
BINARY_NAME=late
VERSION?=2.0.0-rc.1

# Go compiler flags. Commit/build-number/build-date stamping: when git is
# unavailable (tarball checkout, no repo) the commit and build number
# degrade to "unknown" without failing the build.
GIT_COMMIT:=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_NUMBER:=$(shell git rev-list --count HEAD 2>/dev/null || echo unknown)
BUILD_DATE:=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS=-ldflags "-X late/internal/common.Version=${VERSION} -X late/internal/common.BuildNumber=${BUILD_NUMBER} -X late/internal/common.Commit=${GIT_COMMIT} -X 'late/internal/common.BuildDate=${BUILD_DATE}'"

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

vet: ## Catch suspicious Go code
	@echo "Running go vet..."
	@go vet ./...

lint: ## Run linter (golangci-lint)
	@echo "Running golangci-lint..."
	@golangci-lint run ./...

vulncheck: ## Run vulnerability scanner (govulncheck)
	@echo "Running govulncheck..."
	@govulncheck ./...

check: test vet lint vulncheck ## Run all quality and security checks

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
