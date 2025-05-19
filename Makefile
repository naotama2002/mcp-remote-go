.PHONY: build clean test lint install run help

# バイナリ名とバージョン
BINARY_NAME=mcp-remote
VERSION=0.1.0
BUILD_DIR=./bin
MAIN_PATH=./cmd/mcp-remote

# Go コマンド
GO=go
GOFMT=gofmt
GOLINT=golangci-lint

# ビルドフラグ
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

# デフォルトターゲット
all: clean build

# ヘルプ表示
help:
	@echo "利用可能なコマンド:"
	@echo "  make build    - $(BINARY_NAME) をビルドする"
	@echo "  make clean    - ビルドディレクトリをクリーンアップする"
	@echo "  make test     - テストを実行する"
	@echo "  make lint     - コードの静的解析を実行する"
	@echo "  make fmt      - コードフォーマットを適用する"
	@echo "  make install  - $(BINARY_NAME) をインストールする"
	@echo "  make run      - $(BINARY_NAME) を実行する (引数: ARGS=...)"
	@echo "  make release  - リリースビルドを作成する"

# ビルドディレクトリを作成
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# ビルド
build: $(BUILD_DIR)
	$(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "ビルド完了: $(BUILD_DIR)/$(BINARY_NAME)"

# クリーンアップ
clean:
	rm -rf $(BUILD_DIR)
	@echo "クリーンアップ完了"

# テスト実行
test:
	$(GO) test -v ./...

# 静的解析
lint:
	$(GOLINT) run ./...

# コードフォーマット
fmt:
	$(GOFMT) -w -s .

# インストール
install:
	$(GO) install $(LDFLAGS) $(MAIN_PATH)
	@echo "インストール完了: $(BINARY_NAME)"

# 実行
run:
	@if [ -z "$(ARGS)" ]; then \
		echo "使用方法: make run ARGS='https://example.com/mcp [callback-port] [--header \"Header-Name:value\"]'"; \
	else \
		$(GO) run $(MAIN_PATH) $(ARGS); \
	fi

# リリースビルド（複数プラットフォーム向け）
release: $(BUILD_DIR)
	# Linux (amd64)
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)_linux_amd64 $(MAIN_PATH)
	# macOS (amd64)
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)_darwin_amd64 $(MAIN_PATH)
	# macOS (arm64)
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)_darwin_arm64 $(MAIN_PATH)
	# Windows (amd64)
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)_windows_amd64.exe $(MAIN_PATH)
	@echo "リリースビルド完了: $(BUILD_DIR)/"
