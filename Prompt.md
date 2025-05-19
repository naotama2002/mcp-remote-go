Windsurf + Claude 3.7 Sonnet
```
mcp-remote-ts にある TypeScript で実装されたツールを Go で書きたいと思います。

やりたいことは、下記 URL に記載されています。
https://deepwiki.com/geelen/mcp-remote/3.2-mcp-proxy-implementation

MCP の STDIO で受けたデータを http/sse に Proxy する。その際に認証が必要な場合は、MCP の OAuth2.1 仕様に従って認証処理をする。

TypeScript では mcp 部分を `@modelcontextprotocol/sdk` を利用して実装されていますが、Go のmodelcontextprotocol SDK はありません。Go では
@https://github.com/mark3labs/mcp-go が一番ポピュラーだと思いますが、私が理解している中では認証部分の実装がありません。

MCP の OAuth2.1 による認証部分は `@modelcontextprotocol/sdk` の実装や、@https://modelcontextprotocol.io/specification/2025-03-26/basic/authorization の仕様を参照して実装してください。
```
