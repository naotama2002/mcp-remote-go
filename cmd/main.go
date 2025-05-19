package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/naotama2002/mcp-remote-go/client"
)

func main() {
	// コマンドライン引数の解析
	serverURL := flag.String("server", "", "MCP サーバーの URL")
	callbackPort := flag.Int("callback-port", 3000, "OAuth コールバック用のポート")
	transportStrategy := flag.String("transport", "http-first", "トランスポート戦略 (http-first, sse-first, http-only, sse-only)")
	headers := flag.String("headers", "", "リクエストヘッダー (key1=value1,key2=value2)")
	flag.Parse()

	// サーバー URL が指定されていない場合はエラー
	if *serverURL == "" {
		fmt.Println("エラー: サーバー URL を指定してください (-server)")
		flag.Usage()
		os.Exit(1)
	}

	// ヘッダーの解析
	headerMap := make(map[string]string)
	if *headers != "" {
		headerPairs := strings.Split(*headers, ",")
		for _, pair := range headerPairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				headerMap[kv[0]] = kv[1]
			}
		}
	}

	// トランスポート戦略の解析
	var strategy client.TransportStrategy
	switch *transportStrategy {
	case "http-first":
		strategy = client.HTTPFirst
	case "sse-first":
		strategy = client.SSEFirst
	case "http-only":
		strategy = client.HTTPOnly
	case "sse-only":
		strategy = client.SSEOnly
	default:
		fmt.Printf("警告: 不明なトランスポート戦略 '%s'、デフォルトの 'http-first' を使用します\n", *transportStrategy)
		strategy = client.HTTPFirst
	}

	// クライアントオプションの設定
	options := client.ClientOptions{
		ServerURL:         *serverURL,
		CallbackPort:      *callbackPort,
		Headers:           headerMap,
		TransportStrategy: strategy,
	}

	// クライアントの実行
	log.Printf("MCP Remote Go クライアントを開始します (バージョン: %s)\n", client.MCPRemoteVersion)
	log.Printf("サーバー URL: %s\n", *serverURL)
	log.Printf("トランスポート戦略: %s\n", strategy)

	if err := client.RunClient(options); err != nil {
		log.Fatalf("エラー: %v\n", err)
	}
}
