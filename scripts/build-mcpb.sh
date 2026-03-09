#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="mcp-remote-go"
MAIN_DIR="./cmd/mcp-remote-go"
BUILD_DIR="./build"
MCPB_DIR="${BUILD_DIR}/mcpb"
VERSION="${VERSION:-dev}"
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}"

PLATFORMS=(
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

echo "Building .mcpb bundles (version: ${VERSION})..."
mkdir -p "${MCPB_DIR}"

for platform in "${PLATFORMS[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  echo "  Building ${os}/${arch}..."

  bundle_dir="${MCPB_DIR}/${os}-${arch}"
  mkdir -p "${bundle_dir}/server"

  # Determine binary extension and platform name
  ext=""
  platform_name="${os}"
  if [ "${os}" = "windows" ]; then
    ext=".exe"
    platform_name="win32"
  fi

  # Cross-compile
  CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" \
    go build -trimpath -ldflags "${LDFLAGS}" \
    -o "${bundle_dir}/server/${BINARY_NAME}${ext}" "${MAIN_DIR}"

  # Generate manifest.json
  cat > "${bundle_dir}/manifest.json" <<EOF
{
  "manifest_version": "0.3",
  "name": "mcp-remote-go",
  "display_name": "MCP Remote Go",
  "version": "${VERSION}",
  "description": "Proxy local MCP clients to remote MCP servers with Streamable HTTP and SSE transport support",
  "author": {
    "name": "naotama2002"
  },
  "repository": {
    "type": "git",
    "url": "https://github.com/naotama2002/mcp-remote-go"
  },
  "license": "MIT",
  "server": {
    "type": "binary",
    "entry_point": "server/${BINARY_NAME}${ext}",
    "mcp_config": {
      "command": "\${__dirname}/server/${BINARY_NAME}${ext}",
      "args": [
        "--server", "\${user_config.server_url}",
        "--transport", "\${user_config.transport}",
        "--port", "\${user_config.port}"
      ],
      "env": {
        "MCP_ALLOW_HTTP": "\${user_config.allow_http}",
        "MCP_PROXY": "\${user_config.http_proxy}",
        "MCP_AUTH_HEADER": "\${user_config.auth_header}"
      }
    }
  },
  "user_config": {
    "server_url": {
      "type": "string",
      "title": "Remote MCP Server URL",
      "description": "The remote MCP server URL to connect to (e.g. https://example.com/mcp)",
      "required": true
    },
    "transport": {
      "type": "string",
      "title": "Transport Mode",
      "description": "Transport protocol: auto (recommended), streamable-http, or sse",
      "default": "auto"
    },
    "port": {
      "type": "number",
      "title": "OAuth Callback Port",
      "description": "Local port for OAuth callback",
      "default": 3334,
      "min": 1024,
      "max": 65535
    },
    "allow_http": {
      "type": "boolean",
      "title": "Allow HTTP",
      "description": "Allow insecure HTTP connections (only for trusted networks)",
      "default": false
    },
    "http_proxy": {
      "type": "string",
      "title": "HTTP/HTTPS Proxy",
      "description": "Proxy server URL for HTTP/HTTPS connections (e.g. http://proxy:8080)"
    },
    "auth_header": {
      "type": "string",
      "title": "Authorization Header",
      "description": "Custom Authorization header value (e.g. Bearer your-token-here)",
      "sensitive": true
    }
  },
  "compatibility": {
    "platforms": ["${platform_name}"]
  }
}
EOF

  # Package as .mcpb (ZIP)
  mcpb_file="${MCPB_DIR}/${BINARY_NAME}-${os}-${arch}.mcpb"
  (cd "${bundle_dir}" && zip -qr - manifest.json server/) > "${mcpb_file}"

  # Cleanup temp directory
  rm -rf "${bundle_dir}"
  echo "  Created: ${mcpb_file}"
done

echo ""
echo "MCPB build complete. Files:"
ls -lh "${MCPB_DIR}"/*.mcpb
