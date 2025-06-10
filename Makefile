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

.PHONY: all build clean test test-unit test-integration check fmt lint vet help

# Default target
all: clean check build

# Show help
help:
	@echo "Available targets:"
	@echo "  all            - Clean, check, and build"
	@echo "  build          - Build the application"
	@echo "  clean          - Clean build artifacts"
	@echo "  test           - Run safe unit tests (excludes integration tests)"
	@echo "  test-unit      - Run unit tests only (may include some integration tests)"
	@echo "  test-unit-safe - Run only safe unit tests (excludes all integration tests)"
	@echo "  test-integration - Run all tests including browser and integration tests"
	@echo "  check          - Run all code checks (fmt, vet, lint)"
	@echo "  fmt            - Format code"
	@echo "  vet            - Run go vet"
	@echo "  lint           - Run linter"
	@echo "  help           - Show this help"

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

# Run unit tests (excludes browser tests that may open actual browsers)
test-unit:
	@echo "Running unit tests..."
	go test -v ./... -short

# Run all tests including integration tests (may open browsers)
test-integration:
	@echo "Running integration tests (may open browsers)..."
	go test -v ./...

# Run tests (defaults to unit tests for CI safety)
test: test-unit-safe

# Run only safe unit tests (excludes integration tests)
test-unit-safe:
	@echo "Running safe unit tests..."
	go test -v ./auth -short -skip "Integration|Concurrent|Callback"
	go test -v ./proxy -short -skip "Integration|Concurrent|Graceful|Reconnection|ProxyConnection|ProxyWith|ProxyError|ProxySend|Browser"
	go test -v ./cmd/mcp-remote-go -short
	go test -v ./internal/... -short

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
