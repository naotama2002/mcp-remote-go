# MCP Remote Go Makefile

# Variable definitions
BINARY_NAME=mcp-remote-go
VERSION=0.1.0
BUILD_DIR=build
PROXY_PKG=./cmd/proxy
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"
GOARCH=amd64

# Default target
.PHONY: all
all: clean build

# Create build directory
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# Cleanup
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	go clean

# Update dependencies
.PHONY: deps
deps:
	go mod tidy
	go mod download

# Run tests
.PHONY: test
test:
	go test -v ./...

# Build
.PHONY: build
build: $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(PROXY_PKG)

# Development build
.PHONY: dev
dev: clean
	go build -o $(BINARY_NAME) $(PROXY_PKG)

# Install
.PHONY: install
install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/

# Multi-platform build
.PHONY: build-all
build-all: build-linux build-darwin build-windows

.PHONY: build-linux
build-linux: $(BUILD_DIR)
	GOOS=linux GOARCH=$(GOARCH) go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-$(GOARCH) $(PROXY_PKG)

.PHONY: build-darwin
build-darwin: $(BUILD_DIR)
	GOOS=darwin GOARCH=$(GOARCH) go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-$(GOARCH) $(PROXY_PKG)

.PHONY: build-windows
build-windows: $(BUILD_DIR)
	GOOS=windows GOARCH=$(GOARCH) go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-$(GOARCH).exe $(PROXY_PKG)

# Create release packages
.PHONY: release
release: build-all
	mkdir -p $(BUILD_DIR)/release
	tar -czf $(BUILD_DIR)/release/$(BINARY_NAME)-linux-$(GOARCH)-$(VERSION).tar.gz -C $(BUILD_DIR) $(BINARY_NAME)-linux-$(GOARCH)
	tar -czf $(BUILD_DIR)/release/$(BINARY_NAME)-darwin-$(GOARCH)-$(VERSION).tar.gz -C $(BUILD_DIR) $(BINARY_NAME)-darwin-$(GOARCH)
	zip -j $(BUILD_DIR)/release/$(BINARY_NAME)-windows-$(GOARCH)-$(VERSION).zip $(BUILD_DIR)/$(BINARY_NAME)-windows-$(GOARCH).exe

# Help
.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make              - Run cleanup and build"
	@echo "  make clean        - Remove build directory and binaries"
	@echo "  make deps         - Update dependencies"
	@echo "  make test         - Run tests"
	@echo "  make build        - Build executable"
	@echo "  make dev          - Development build (output to current directory)"
	@echo "  make install      - Install binary to GOPATH"
	@echo "  make build-all    - Build for all platforms"
	@echo "  make release      - Create release packages"
	@echo "  make help         - Display this help"
