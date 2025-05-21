.PHONY: build run clean test

# Binary name
BINARY_NAME=mcp-remote-go

# Build directory
BUILD_DIR=bin

# Build the application
build:
	@mkdir -p $(BUILD_DIR)
	@echo "Building..."
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) .

# Run the application
run:
	@go run main.go $(ARGS)

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Build for multiple platforms
build-all: clean
	@mkdir -p $(BUILD_DIR)
	@echo "Building for multiple platforms..."
	
	@echo "Building for Linux (amd64)..."
	@GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 .
	
	@echo "Building for macOS (amd64)..."
	@GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 .
	
	@echo "Building for macOS (arm64)..."
	@GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 .
	
	@echo "Building for Windows (amd64)..."
	@GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe .

# Install the application to $GOPATH/bin
install: build
	@echo "Installing..."
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/ 