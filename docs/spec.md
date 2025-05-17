# MCP Remote Server Go 実装仕様

## 概要

MCP Remote Server は、Model Context Protocol (MCP) に準拠したリモートサーバーとの通信を可能にするプロキシツールです。このドキュメントでは、TypeScript 実装を Go 言語で再実装するための仕様を定義します。

## 主要コンポーネント

### 1. プロキシ (Proxy)

TypeScript の `proxy.ts` に相当する Go 実装です。

#### 機能
- ローカル STDIO MCP サーバーとリモート SSE サーバー間の双方向プロキシ
- OAuth 認証フローの処理
- トランスポート間のメッセージ転送

#### コマンドライン引数
```
mcp-remote-go <https://server-url> [callback-port]
```

#### オプション
- `--header`, `-H`: リクエストに追加するヘッダー (複数指定可)
- `--transport`: 使用するトランスポート戦略 (`sse-only`, `http-only`, `sse-first`, `http-first`)

### 3. 認証 (Authentication)

#### OAuth クライアントプロバイダー
TypeScript の `node-oauth-client-provider.ts` に相当する Go 実装です。

#### 機能
- OAuth 認証フローの処理
- クライアント情報とトークンの保存と取得
- PKCE (Proof Key for Code Exchange) の処理
- ブラウザでの認証 URL オープン

### 4. 認証コーディネーション (Auth Coordination)

TypeScript の `coordination.ts` に相当する Go 実装です。

#### 機能
- 複数のクライアント/プロキシインスタンス間での認証の調整
- ロックファイルを使用した認証プロセスの管理
- 認証コールバックサーバーの設定

### 5. ユーティリティ (Utilities)

TypeScript の `utils.ts` に相当する Go 実装です。

#### 機能
- トランスポート間の双方向プロキシ設定
- リモートサーバーへの接続処理
- OAuth コールバックサーバーの設定
- コマンドライン引数の解析
- シグナルハンドラの設定
- サーバー URL のハッシュ生成

## データ構造

### 1. トランスポート戦略
```go
type TransportStrategy string

const (
    SSEOnly   TransportStrategy = "sse-only"
    HTTPOnly  TransportStrategy = "http-only"
    SSEFirst  TransportStrategy = "sse-first"
    HTTPFirst TransportStrategy = "http-first"
)
```

### 2. OAuth トークン
```go
type OAuthTokens struct {
    AccessToken  string    `json:"access_token"`
    TokenType    string    `json:"token_type"`
    RefreshToken string    `json:"refresh_token,omitempty"`
    ExpiresIn    int       `json:"expires_in,omitempty"`
    ExpiresAt    time.Time `json:"expires_at,omitempty"`
}
```

### 3. OAuth クライアント情報
```go
type OAuthClientInformation struct {
    ClientID                string   `json:"client_id"`
    ClientSecret            string   `json:"client_secret,omitempty"`
    RedirectURIs            []string `json:"redirect_uris"`
    TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
    GrantTypes              []string `json:"grant_types"`
    ResponseTypes           []string `json:"response_types"`
    ClientName              string   `json:"client_name"`
    ClientURI               string   `json:"client_uri,omitempty"`
    SoftwareID              string   `json:"software_id,omitempty"`
    SoftwareVersion         string   `json:"software_version,omitempty"`
}
```

### 4. ロックファイルデータ
```go
type LockfileData struct {
    PID       int   `json:"pid"`
    Port      int   `json:"port"`
    Timestamp int64 `json:"timestamp"`
}
```

## インターフェース

### 1. トランスポートインターフェース
```go
type Transport interface {
    Start() error
    Close() error
    Send(message interface{}) error
    SetMessageHandler(handler func(message interface{}))
    SetErrorHandler(handler func(err error))
    SetCloseHandler(handler func())
}
```

### 2. OAuth クライアントプロバイダーインターフェース
```go
type OAuthClientProvider interface {
    ClientInformation() (*OAuthClientInformation, error)
    SaveClientInformation(clientInfo *OAuthClientInformation) error
    Tokens() (*OAuthTokens, error)
    SaveTokens(tokens *OAuthTokens) error
    RedirectToAuthorization(authorizationURL *url.URL) error
    SaveCodeVerifier(codeVerifier string) error
    CodeVerifier() (string, error)
    RedirectURL() string
    ClientMetadata() map[string]interface{}
}
```

### 3. 認証コーディネーターインターフェース
```go
type AuthCoordinator interface {
    InitializeAuth() (*AuthState, error)
}

type AuthState struct {
    Server         *http.Server
    WaitForAuthCode func() (string, error)
    SkipBrowserAuth bool
}
```

## 設定管理

### 1. 設定ファイルパス
- ホームディレクトリの `.mcp` フォルダに設定ファイルを保存
- サーバー URL ごとに個別のサブフォルダを作成

### 2. 保存するファイル
- `client_info.json`: OAuth クライアント情報
- `tokens.json`: OAuth トークン
- `code_verifier.txt`: PKCE コード検証子
- `lock`: 認証プロセスのロックファイル

## エラー処理

### 1. 認証エラー
- 認証が必要な場合は OAuth フローを開始
- トークンの有効期限が切れた場合は自動的に更新

### 2. トランスポートエラー
- トランスポート戦略に基づいて別のトランスポートにフォールバック
- 接続エラーの場合は適切なエラーメッセージを表示

### 3. プロセス間調整エラー
- ロックファイルが無効な場合は削除して新しいプロセスを開始
- 他のプロセスからの認証完了を待機

## セキュリティ考慮事項

### 1. トークン保存
- トークンはローカルファイルシステムに保存
- 適切なファイルパーミッションを設定

### 2. PKCE
- 認証コードフローでの PKCE を実装
- コード検証子はセキュアに保存

## 実装の注意点

### 1. 互換性
- TypeScript 実装との完全な互換性を維持
- 同じコマンドライン引数とオプションをサポート

### 2. パフォーマンス
- 効率的なリソース使用
- 適切なゴルーチン管理

### 3. クロスプラットフォーム
- Windows、macOS、Linux をサポート
- プラットフォーム固有の違いを適切に処理

## 開発ロードマップ

1. 基本的なデータ構造とインターフェースの実装
2. OAuth 認証フローの実装
3. トランスポート (SSE と HTTP) の実装
4. クライアントとプロキシコマンドの実装
5. 認証コーディネーションの実装
6. テストとドキュメント作成
7. パフォーマンス最適化
