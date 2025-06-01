# MCP Remote for Go

This Go implementation allows MCP clients that only support local (stdio) connections to connect to remote MCP servers with authentication support.

## Overview

MCP Remote proxies between:
- A local MCP client using stdio transport
- A remote MCP server using Server-Sent Events (SSE) with OAuth authentication

## Installation

### Building from Source

```bash
# Clone the repository
git clone https://github.com/naotama2002/mcp-remote-go.git
cd mcp-remote-go

# Build the binary
make build
```

### Using Docker

You can also run `mcp-remote-go` using Docker, which provides a consistent environment and easier deployment.

#### Pull from GitHub Container Registry

```bash
# Pull the latest image
docker pull ghcr.io/naotama2002/mcp-remote-go:latest

# Or pull a specific version
docker pull ghcr.io/naotama2002/mcp-remote-go:{TAG}
```

#### Build locally

```bash
# Build the Docker image
docker build -t mcp-remote-go .
```

## Usage

### Binary Usage

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

### Docker Usage

```bash
# Basic usage with Docker
docker run --rm -it -p 3334:3334 ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse

# With custom port for OAuth callback
docker run --rm -it -p 9090:9090 ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse 9090

# With custom headers
docker run --rm -it -p 3334:3334 ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse --header "Authorization: Bearer YOUR_TOKEN"

# Allow HTTP for trusted networks
docker run --rm -it -p 3334:3334 ghcr.io/naotama2002/mcp-remote-go:latest http://internal.mcp.server/sse --allow-http

# Mount auth directory to persist OAuth tokens
docker run --rm -it -p 3334:3334 -v ~/.mcp-auth:/home/appuser/.mcp-auth ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse
```

## Configuration for MCP Clients

### Claude Desktop

Edit the configuration file at:
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

#### Using Binary

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

#### Using Docker

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "docker",
      "args": [
        "run",
        "--rm",
        "-i",
        "--net=host",
        "ghcr.io/naotama2002/mcp-remote-go:latest",
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

For persistent OAuth tokens with Docker:

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "docker",
      "args": [
        "run",
        "--rm",
        "-i",
        "--net=host",
        "-v",
        "~/.mcp-auth:/home/appuser/.mcp-auth",
        "ghcr.io/naotama2002/mcp-remote-go:latest",
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

### Cursor

Edit the configuration file at `~/.cursor/mcp.json`:

#### Using Binary

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

#### Using Docker

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "docker",
      "args": [
        "run",
        "--rm",
        "-i",
        "--net=host",
        "ghcr.io/naotama2002/mcp-remote-go:latest",
        "https://remote.mcp.server/sse"
      ]
    }
  }
}
```

### Windsurf

Edit the configuration file at `~/.codeium/windsurf/mcp_config.json`:

#### Using Binary

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

#### Using Docker

```json
{
  "mcpServers": {
    "remote-example": {
      "command": "docker",
      "args": [
        "run",
        "--rm",
        "-i",
        "--net=host",
        "ghcr.io/naotama2002/mcp-remote-go:latest",
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

### Docker Issues

#### Port Already in Use

If you get a "port already in use" error, either stop the conflicting service or use a different port:

```bash
# Use a different port
docker run --rm -it -p 3335:3334 ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse
```

#### Permission Issues with Volume Mount

If you have permission issues when mounting the auth directory:

```bash
# Make sure the directory exists and has proper permissions
mkdir -p ~/.mcp-auth
chmod 755 ~/.mcp-auth

# Run with volume mount
docker run --rm -it -p 3334:3334 -v ~/.mcp-auth:/home/appuser/.mcp-auth ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse
```

#### Network Issues with MCP Clients

If MCP clients can't connect to the Docker container, try using host networking:

```bash
# Use host networking mode
docker run --rm -i --net=host ghcr.io/naotama2002/mcp-remote-go:latest https://remote.mcp.server/sse
```

## License

MIT 
