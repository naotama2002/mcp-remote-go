# mcp-remote-go セキュリティレビュー結果

## レビュー概要

`mcp-remote-go`はMCP（Model Context Protocol）のstdioトランスポートをSSE（Server-Sent Events）経由のリモート接続に変換するプロキシツールです。

レビュー日: 2024年12月

**更新履歴:**
- 2024年12月: `github.com/pkg/browser`パッケージを使用した修正を実装

## レビュー結果

### 1. descriptionやpromptに不審な指示や埋込箇所などがないこと

**評価: 問題なし**

- プロンプトや説明文などの指示系フィールドは実装されていません
- このプロキシは単純にメッセージを転送するだけで、内容を解釈していません

### 2. 不審な外への通信やコマンド実行などの内部動作をするようなコードがないこと

**評価: ✅ 修正済み**（以前は重大な脆弱性あり）

#### 2.1 外部通信先の制限

**問題点:**
- 任意のURLへの接続が可能で、ホワイトリストやbaseURL制限が実装されていない
- `--allow-http`フラグでHTTP接続も許可可能

**推奨事項:**
- 接続可能なドメインのホワイトリスト機能を追加
- 社内利用の場合は、内部ドメインのみに制限する設定を追加

#### 2.2 コマンド実行の脆弱性

**✅ 修正済み: OSコマンドインジェクションの脆弱性は解決されました**

**修正前の問題:**
```go
// openBrowser opens the specified URL in the default browser
func openBrowser(url string) error {
    var cmd string
    var args []string

    switch runtime.GOOS {
    case "windows":
        cmd = "cmd"
        args = []string{"/c", "start", url}  // ⚠️ OSコマンドインジェクションの脆弱性
    case "darwin":
        cmd = "open"
        args = []string{url}
    default:
        cmd = "xdg-open"
        args = []string{url}
    }

    return exec.Command(cmd, args...).Start()
}
```

**実装された修正（[github.com/pkg/browser](https://pkg.go.dev/github.com/pkg/browser)を使用）:**
```go
// openBrowser opens the specified URL in the default browser
func openBrowser(rawURL string) error {
	// URLの検証
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// スキームの制限（HTTPとHTTPSのみ許可）
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("only http and https URLs are allowed")
	}

	// github.com/pkg/browser を使用してブラウザを開く
	return browser.OpenURL(rawURL)
}
```

**修正の利点:**
1. **OSコマンドインジェクションの完全な防止**: `github.com/pkg/browser`は内部で安全な実装を使用
2. **URL検証**: 不正なURLを事前に検出
3. **スキーム制限**: HTTPとHTTPSのみを許可し、`file://`や`javascript:`などの危険なスキームをブロック
4. **メンテナンス性**: 広く使用されている信頼できるライブラリを使用

### 3. MCPホストに対して不審な指示をするような返答を返すコードがないこと

**評価: 問題なし**

- サーバーからのメッセージは検証や変更なしにそのまま転送
- メッセージ内容の解釈や加工は行われない

### 4. 副作用のある操作機能がある場合、それらをAIが実行して良いことの確認が取れていること

**評価: 該当なし**

- このツール自体は副作用のある操作を提供していない
- 単純なプロキシとして動作

### 5. secretが適切に扱われていること

**評価: 概ね適切**

**良い点:**
- トークンファイルは0600パーミッションで保存
- トークンは`~/.mcp-remote-go-auth/`ディレクトリに保存
- client_secretはオプション（なしでも動作可能）

**改善点:**
- トークンの暗号化保存を検討
- 環境変数経由でのトークン設定オプションの追加を検討

### 6. URI やファイルパスは正規化されていること

**評価: 適切**

- ファイルパスは`filepath.Join`を使用して構築
- serverURLHashはSHA256ハッシュを使用（Path Traversal不可）

### 7. Path Traversal や SSRF を防ぐ制限が設けられていること

**評価: 部分的に問題あり**

**Path Traversal: 問題なし**
- ファイルパスはハッシュ値を使用して構築
- ユーザー入力が直接ファイルパスに使用されない

**SSRF: 制限なし**
- 任意のURLへの接続が可能
- 内部ネットワークへのアクセス制限なし

**推奨事項:**
- プライベートIPアドレス範囲へのアクセスを制限
- URLスキームを制限（https://のみ、または明示的な許可）

## 追加のセキュリティ観点

### 8. 依存関係

**評価: 良好**

- 最小限の外部依存（`github.com/pkg/browser`のみ）
- `github.com/pkg/browser`は広く使用されている信頼できるライブラリ
- サプライチェーン攻撃のリスクが低い

### 9. Docker設定

**評価: 適切**

- 非rootユーザーで実行
- マルチステージビルドを使用
- 最小限の権限で動作

## 総合評価とアクションアイテム

### ✅ 解決済みの問題

1. **OSコマンドインジェクションの脆弱性**
   - `github.com/pkg/browser`パッケージの採用により完全に解決

### 中程度の問題（早期対応を推奨）

2. **外部通信先の制限なし**
   - ドメインホワイトリスト機能の実装
   - プライベートIPアドレスへのアクセス制限

3. **HTTP接続の許可**
   - デフォルトではHTTPSのみに制限（現状の実装で問題なし）
   - `--allow-http`の使用は信頼できる内部ネットワークのみに限定することを文書化

### 推奨改善事項

4. **監査ログの追加**
   - 接続先URL、認証情報の使用などをログに記録

5. **設定ファイルによる制限**
   - 許可されたドメインリストを設定ファイルで管理
   - 環境変数での設定オプション追加

## 社内利用のための推奨設定

1. **ネットワーク制限**
   - ファイアウォールで接続可能なドメインを制限
   - プロキシサーバー経由での通信に限定

2. **実行環境の制限**
   - Dockerコンテナでの実行を推奨
   - 適切なセキュリティグループ/ネットワークポリシーの設定

3. **モニタリング**
   - アクセスログの監視
   - 異常な接続先の検知

## 結論

`mcp-remote-go`の最も重大なセキュリティ問題である**OSコマンドインジェクションの脆弱性**は、`github.com/pkg/browser`パッケージの採用により解決されました。

社内での利用は可能ですが、以下の条件で使用することを推奨します：
1. 外部通信先の制限（ファイアウォールレベルまたはアプリケーションレベル）
2. HTTPSのみの使用（`--allow-http`は使用しない）
3. 適切なログ監視の実装

これらの追加対策により、より安全な運用が可能になります。 