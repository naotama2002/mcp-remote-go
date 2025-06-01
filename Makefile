# MCP Remote Go Makefile

# Variables
BINARY_NAME=mcp-remote-go
BUILD_DIR=./build
MAIN_DIR=./cmd/mcp-remote-go
GOFLAGS=-trimpath

# Get the current git commit hash
GIT_COMMIT=$(shell git rev-parse --short HEAD)
BUILD_TIME=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
VERSION?=dev

# LDFLAGS
LDFLAGS=-ldflags "-X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}"

.PHONY: all build clean test check fmt lint vet

# Default target
all: clean check build

# Build the application
build:
	@echo "Building ${BINARY_NAME}..."
	@mkdir -p ${BUILD_DIR}
	go build ${GOFLAGS} ${LDFLAGS} -o ${BUILD_DIR}/${BINARY_NAME} ${MAIN_DIR}
	@echo "Build complete: ${BUILD_DIR}/${BINARY_NAME}"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf ${BUILD_DIR}
	@go clean
	@echo "Clean complete"

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run all checks (fmt, vet, lint)
check: fmt vet lint

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Run go vet
vet:
	@echo "Running go vet..."
	go vet ./...

# Run linter (requires golangci-lint)
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed. Skipping lint."; \
		echo "To install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi
