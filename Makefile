# MCP-Remote-Go Makefile

# 変数定義
BINARY_NAME=mcp-remote-go
GO=go
GOFLAGS=-ldflags="-s -w"
BUILD_DIR=./build
SRC_DIR=.
MAIN_FILE=./cmd/main.go

# デフォルトターゲット
.PHONY: all
all: build

# ビルドディレクトリの作成
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# ビルド
.PHONY: build
build: $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_FILE)

# 開発用ビルド（デバッグ情報付き）
.PHONY: build-dev
build-dev: $(BUILD_DIR)
	$(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-dev $(MAIN_FILE)

# テスト実行
.PHONY: test
test:
	$(GO) test -v ./...

# テストカバレッジ
.PHONY: test-coverage
test-coverage:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

# 依存関係の更新
.PHONY: deps
deps:
	$(GO) mod tidy
	$(GO) mod verify

# クリーンアップ
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# インストール
.PHONY: install
install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/

# 実行
.PHONY: run
run:
	$(GO) run $(MAIN_FILE)

# ヘルプ
.PHONY: help
help:
	@echo "使用可能なコマンド:"
	@echo "  make build         - リリース用バイナリをビルド"
	@echo "  make build-dev     - 開発用バイナリをビルド（デバッグ情報付き）"
	@echo "  make test          - テストを実行"
	@echo "  make test-coverage - テストカバレッジを計測"
	@echo "  make deps          - 依存関係を更新"
	@echo "  make clean         - ビルド成果物を削除"
	@echo "  make install       - バイナリをGOPATH/binにインストール"
	@echo "  make run           - プログラムを実行"
	@echo "  make help          - このヘルプメッセージを表示"
