# MCP Remote Go

## 概要

MCP Remote Go は、Model Context Protocol (MCP) のプロキシツールです。ローカルの STDIO ベースの MCP クライアントと、リモートの HTTP/SSE ベースの MCP サーバー間の通信を仲介します。特に OAuth2.1 認証機能を実装しており、認証が必要なリモート MCP サーバーとの通信をサポートします。

このプロジェクトは、TypeScript で実装された [mcp-remote](https://deepwiki.com/geelen/mcp-remote/3.2-mcp-proxy-implementation) の Go 言語版です。

## 主な機能

- STDIO と HTTP/SSE 間のプロキシ機能
- OAuth2.1 認証サポート（PKCE フロー）
- 複数インスタンス間での認証状態の調整
- トランスポート自動選択と自動フォールバック
- VPN 環境などでの証明書エラー対応

## 技術スタック

- 言語: Go
- 依存ライブラリ:
  - [github.com/mark3labs/mcp-go](https://github.com/mark3labs/mcp-go): MCP の Go 実装
  - [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2): OAuth2 認証
  - [github.com/gin-gonic/gin](https://github.com/gin-gonic/gin): OAuth コールバックサーバー

## インストール

```bash
go install github.com/naotama2002/mcp-remote-go/cmd/mcp-remote@latest
```

または、リポジトリをクローンして直接ビルドすることもできます：

```bash
git clone https://github.com/naotama2002/mcp-remote-go.git
cd mcp-remote-go
go build -o mcp-remote ./cmd/mcp-remote
```

## 使い方

```bash
mcp-remote <https://server-url> [callback-port] [--header "Header-Name:value"]
```

### 引数

- `server-url`: リモート MCP サーバーの URL（必須）
- `callback-port`: OAuth コールバックサーバーのポート（オプション、デフォルト: 自動選択）

### オプション

- `--header`, `-H`: リクエストにカスタムヘッダーを追加（複数指定可能）
- `--allow-http`: HTTPS 以外の接続を許可（localhost 以外では非推奨）
- `--transport`: トランスポート戦略（sse-only, http-only, sse-first, http-first）
- `--help`, `-h`: ヘルプメッセージを表示

### 例

```bash
# 基本的な使用方法
mcp-remote https://example.com/mcp

# コールバックポートを指定
mcp-remote https://example.com/mcp 8080

# カスタムヘッダーを追加
mcp-remote https://example.com/mcp --header "X-API-Key:your-api-key"

# トランスポート戦略を指定
mcp-remote https://example.com/mcp --transport sse-only
```

## 認証

MCP Remote Go は OAuth2.1 認証をサポートしています。初回接続時に認証が必要な場合、ブラウザが自動的に開き、認証ページにリダイレクトされます。認証情報は `~/.mcp-auth` ディレクトリに保存され、以降の接続で再利用されます。

複数のプロキシインスタンスが同時に実行されている場合、認証状態は共有され、一度の認証で全てのインスタンスが接続できるように調整されます。

## VPN 環境での使用

VPN 環境などで証明書エラーが発生する場合、環境変数 `SSL_CERT_FILE` を設定することで解決できます：

```bash
SSL_CERT_FILE=/path/to/ca-cert.pem mcp-remote https://example.com/mcp
```

## ライセンス

MIT

## 参考

- [MCP Proxy Implementation](https://deepwiki.com/geelen/mcp-remote/3.2-mcp-proxy-implementation)
- [MCP OAuth2.1 Authorization](https://modelcontextprotocol.io/specification/2025-03-26/basic/authorization)
- [mcp-go](https://github.com/mark3labs/mcp-go)
