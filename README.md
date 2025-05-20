MCP クライアントから STDIO を MCP 仕様に従った認証つき SSE サーバに Proxy するプログラムを Go言語で作成してください

# MCP Remote for Go

This Go implementation allows MCP clients that only support local (stdio) connections to connect to remote MCP servers with authentication support.

## Overview

MCP Remote proxies between:
- A local MCP client using stdio transport
- A remote MCP server using Server-Sent Events (SSE) with OAuth authentication

## Installation

```bash
# Clone the repository
git clone https://github.com/naotama2002/mcp-remote-go.git
cd mcp-remote-go

# Build the binary
go build -o bin/mcp-remote-go

# Install to your PATH (optional)
# cp bin/mcp-remote-go /usr/local/bin/
```

## Usage

```bash
# Basic usage
mcp-remote-go https://remote.mcp.server/sse

# With custom port for OAuth callback
mcp-remote-go https://remote.mcp.server/sse 9090

# With custom headers (useful for auth bypass or adding auth tokens)
mcp-remote-go https://remote.mcp.server/sse --header "Authorization: Bearer YOUR_TOKEN"

# Allow HTTP for trusted networks (normally HTTPS is required)
mcp-remote-go http://internal.mcp.server/sse --allow-http
```

## Configuration for MCP Clients

### Claude Desktop

Edit the configuration file at:
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

Add the following:

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "/path/to/mcp-remote-go",
      "args": [
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

### Cursor

Edit the configuration file at `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "/path/to/mcp-remote-go",
      "args": [
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

### Windsurf

Edit the configuration file at `~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "/path/to/mcp-remote-go",
      "args": [
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

## Authentication

The first time you connect to a server requiring authentication, you'll be prompted to open a URL in your browser to authorize access. The program will wait for you to complete the OAuth flow and then establish the connection.

Authorization tokens are stored in `~/.mcp-auth/` and will be reused for future connections.

## Troubleshooting

### Clear Authentication Data

If you're having issues with authentication, you can clear the stored data:

```bash
rm -rf ~/.mcp-auth
```

### VPN/Certificate Issues

If you're behind a VPN and experiencing certificate issues, you might need to specify CA certificates:

```bash
export SSL_CERT_FILE=/path/to/ca-certificates.crt
mcp-remote-go https://remote.mcp.server/sse
```

## License

MIT 