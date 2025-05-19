package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/naotama2002/mcp-remote-go/auth"
	"github.com/naotama2002/mcp-remote-go/proxy"
)

const usage = `Usage: mcp-remote <https://server-url> [callback-port] [--header "Header-Name:value"]

Arguments:
  server-url       The URL of the remote MCP server (required)
  callback-port    Port for OAuth callback server (optional, default: auto-select)

Options:
  --header, -H     Add custom header to requests (can be used multiple times)
  --allow-http     Allow non-HTTPS connections (not recommended except for localhost)
  --transport      Transport strategy: sse-only, http-only, sse-first, http-first (default: http-first)
  --help, -h       Show this help message
`

func main() {
	log.SetPrefix("[mcp-remote] ")

	// フラグの定義
	var (
		allowHTTP      bool
		headers        headerFlags
		transportFlag  string
		showHelp       bool
	)

	flag.BoolVar(&allowHTTP, "allow-http", false, "Allow non-HTTPS connections")
	flag.Var(&headers, "header", "Add custom header to requests")
	flag.Var(&headers, "H", "Add custom header to requests (shorthand)")
	flag.StringVar(&transportFlag, "transport", "http-first", "Transport strategy: sse-only, http-only, sse-first, http-first")
	flag.BoolVar(&showHelp, "help", false, "Show help message")
	flag.BoolVar(&showHelp, "h", false, "Show help message (shorthand)")

	// コマンドライン引数の解析
	flag.Parse()

	if showHelp {
		fmt.Print(usage)
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) < 1 {
		fmt.Print(usage)
		os.Exit(1)
	}

	// サーバーURLの検証
	serverURL := args[0]
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		log.Fatalf("Invalid server URL: %v", err)
	}

	// HTTPSの検証（localhost以外）
	if !allowHTTP && parsedURL.Scheme != "https" && !isLocalhost(parsedURL.Hostname()) {
		log.Fatalf("Error: Non-HTTPS URLs are not allowed for security reasons. Use --allow-http to override.")
	}

	// コールバックポートの解析
	callbackPort := 0
	if len(args) > 1 {
		_, err := fmt.Sscanf(args[1], "%d", &callbackPort)
		if err != nil {
			log.Fatalf("Invalid callback port: %v", err)
		}
	}

	// 利用可能なポートが指定されていない場合は自動選択
	if callbackPort == 0 {
		callbackPort = auth.FindAvailablePort(0)
	}

	// トランスポート戦略の解析
	var transportStrategy proxy.TransportStrategy
	switch strings.ToLower(transportFlag) {
	case "sse-only":
		transportStrategy = proxy.SSEOnly
	case "http-only":
		transportStrategy = proxy.HTTPOnly
	case "sse-first":
		transportStrategy = proxy.SSEFirst
	case "http-first":
		transportStrategy = proxy.HTTPFirst
	default:
		log.Fatalf("Invalid transport strategy: %s", transportFlag)
	}

	// ヘッダーのマップを作成
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			log.Fatalf("Invalid header format: %s (should be 'Name:Value')", h)
		}
		headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	// プロキシの実行
	log.Printf("Starting MCP proxy for %s (callback port: %d)", serverURL, callbackPort)
	err = proxy.RunProxy(serverURL, callbackPort, headerMap, transportStrategy)
	if err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}

// headerFlags はカスタムヘッダーのフラグを処理するための型
type headerFlags []string

func (h *headerFlags) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// isLocalhost はホスト名がローカルホストかどうかを判断します
func isLocalhost(hostname string) bool {
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1"
}
