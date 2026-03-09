package main

import (
	"testing"
)

func TestParseRemainingArgs_TransportAfterURL(t *testing.T) {
	// Simulates: mcp-remote-go https://example.com/mcp --transport streamable-http
	remaining := []string{"https://example.com/mcp", "--transport", "streamable-http"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "https://example.com/mcp" {
		t.Errorf("Expected server URL 'https://example.com/mcp', got '%s'", cfg.serverURL)
	}
	if cfg.transportMode != "streamable-http" {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", cfg.transportMode)
	}
}

func TestParseRemainingArgs_TransportBeforeURL(t *testing.T) {
	// When flag.Parse() handles --transport before URL, remaining only has the URL
	remaining := []string{"https://example.com/mcp"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "streamable-http", // already parsed by flag.Parse()
	})

	if cfg.serverURL != "https://example.com/mcp" {
		t.Errorf("Expected server URL 'https://example.com/mcp', got '%s'", cfg.serverURL)
	}
	if cfg.transportMode != "streamable-http" {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", cfg.transportMode)
	}
}

func TestParseRemainingArgs_TransportEqualsForm(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "--transport=sse"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.transportMode != "sse" {
		t.Errorf("Expected transport mode 'sse', got '%s'", cfg.transportMode)
	}
}

func TestParseRemainingArgs_AllowHTTPAfterURL(t *testing.T) {
	remaining := []string{"http://localhost:8080/mcp", "--allow-http"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "http://localhost:8080/mcp" {
		t.Errorf("Expected server URL 'http://localhost:8080/mcp', got '%s'", cfg.serverURL)
	}
	if !cfg.allowHTTP {
		t.Error("Expected allowHTTP to be true")
	}
}

func TestParseRemainingArgs_HeaderAfterURL(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "--header", "Authorization:Bearer token123"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if len(cfg.headers) != 1 || cfg.headers[0] != "Authorization:Bearer token123" {
		t.Errorf("Expected header 'Authorization:Bearer token123', got %v", cfg.headers)
	}
}

func TestParseRemainingArgs_PortAfterURL(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "--port", "9090"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.callbackPort != 9090 {
		t.Errorf("Expected callback port 9090, got %d", cfg.callbackPort)
	}
}

func TestParseRemainingArgs_PositionalPort(t *testing.T) {
	// Simulates: mcp-remote-go https://example.com/mcp 9090
	remaining := []string{"https://example.com/mcp", "9090"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "https://example.com/mcp" {
		t.Errorf("Expected server URL 'https://example.com/mcp', got '%s'", cfg.serverURL)
	}
	if cfg.callbackPort != 9090 {
		t.Errorf("Expected callback port 9090, got %d", cfg.callbackPort)
	}
}

func TestParseRemainingArgs_MultipleFlags(t *testing.T) {
	// Simulates: mcp-remote-go https://example.com/mcp --transport streamable-http --header "X-Key:val" --allow-http
	remaining := []string{
		"https://example.com/mcp",
		"--transport", "streamable-http",
		"--header", "X-Key:val",
		"--allow-http",
	}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "https://example.com/mcp" {
		t.Errorf("Expected server URL 'https://example.com/mcp', got '%s'", cfg.serverURL)
	}
	if cfg.transportMode != "streamable-http" {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", cfg.transportMode)
	}
	if len(cfg.headers) != 1 || cfg.headers[0] != "X-Key:val" {
		t.Errorf("Expected header 'X-Key:val', got %v", cfg.headers)
	}
	if !cfg.allowHTTP {
		t.Error("Expected allowHTTP to be true")
	}
}

func TestParseRemainingArgs_NoArgs(t *testing.T) {
	cfg := parseRemainingArgs([]string{}, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "" {
		t.Errorf("Expected empty server URL, got '%s'", cfg.serverURL)
	}
	if cfg.transportMode != "auto" {
		t.Errorf("Expected transport mode 'auto', got '%s'", cfg.transportMode)
	}
}

func TestParseRemainingArgs_ServerURLFromFlag(t *testing.T) {
	// Server URL already set by flag.Parse(), remaining has transport only
	remaining := []string{"--transport", "sse"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		serverURL:     "https://already-set.com/mcp",
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.serverURL != "https://already-set.com/mcp" {
		t.Errorf("Expected server URL 'https://already-set.com/mcp', got '%s'", cfg.serverURL)
	}
	if cfg.transportMode != "sse" {
		t.Errorf("Expected transport mode 'sse', got '%s'", cfg.transportMode)
	}
}

func TestParseRemainingArgs_SingleDashFlags(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "-transport", "streamable-http"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.transportMode != "streamable-http" {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", cfg.transportMode)
	}
}

func TestApplyEnvOverrides_ServerURL(t *testing.T) {
	t.Setenv("MCP_SERVER_URL", "https://env.example.com/mcp")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if serverURL != "https://env.example.com/mcp" {
		t.Errorf("Expected server URL from env, got '%s'", serverURL)
	}
}

func TestApplyEnvOverrides_CLITakesPrecedence(t *testing.T) {
	t.Setenv("MCP_SERVER_URL", "https://env.example.com/mcp")
	t.Setenv("MCP_TRANSPORT", "sse")

	serverURL := "https://cli.example.com/mcp"
	port := 3334
	allowHTTP := false
	transport := "streamable-http"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if serverURL != "https://cli.example.com/mcp" {
		t.Errorf("CLI server URL should take precedence, got '%s'", serverURL)
	}
	if transport != "streamable-http" {
		t.Errorf("CLI transport should take precedence, got '%s'", transport)
	}
}

func TestApplyEnvOverrides_AllowHTTP(t *testing.T) {
	t.Setenv("MCP_ALLOW_HTTP", "true")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if !allowHTTP {
		t.Error("Expected allowHTTP to be true from env")
	}
}

func TestApplyEnvOverrides_AuthHeader(t *testing.T) {
	t.Setenv("MCP_AUTH_HEADER", "Bearer secret-token")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if len(headers) != 1 || headers[0] != "Authorization:Bearer secret-token" {
		t.Errorf("Expected Authorization header, got %v", headers)
	}
}

func TestApplyEnvOverrides_Port(t *testing.T) {
	t.Setenv("MCP_PORT", "9090")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if port != 9090 {
		t.Errorf("Expected port 9090 from env, got %d", port)
	}
}

func TestApplyEnvOverrides_NoEnvVars(t *testing.T) {
	t.Setenv("MCP_SERVER_URL", "")
	t.Setenv("MCP_TRANSPORT", "")
	t.Setenv("MCP_PORT", "")
	t.Setenv("MCP_HTTPS_PROXY", "")
	t.Setenv("MCP_ALLOW_HTTP", "")
	t.Setenv("MCP_AUTH_HEADER", "")

	serverURL := "https://original.com/mcp"
	port := 3334
	allowHTTP := false
	transport := "auto"
	headers := flagList{}

	httpProxy := ""
	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if serverURL != "https://original.com/mcp" {
		t.Errorf("Server URL should be unchanged, got '%s'", serverURL)
	}
	if port != 3334 {
		t.Errorf("Port should be unchanged, got %d", port)
	}
	if allowHTTP {
		t.Error("allowHTTP should be false")
	}
	if transport != "auto" {
		t.Errorf("Transport should be unchanged, got '%s'", transport)
	}
	if len(headers) != 0 {
		t.Errorf("Headers should be empty, got %v", headers)
	}
}

func TestParseRemainingArgs_ProxyAfterURL(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "--proxy", "http://proxy:8080"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.httpProxy != "http://proxy:8080" {
		t.Errorf("Expected proxy 'http://proxy:8080', got '%s'", cfg.httpProxy)
	}
}

func TestParseRemainingArgs_ProxyEqualsForm(t *testing.T) {
	remaining := []string{"https://example.com/mcp", "--proxy=http://proxy:3128"}
	cfg := parseRemainingArgs(remaining, cliConfig{
		callbackPort:  3334,
		transportMode: "auto",
	})

	if cfg.httpProxy != "http://proxy:3128" {
		t.Errorf("Expected proxy 'http://proxy:3128', got '%s'", cfg.httpProxy)
	}
}

func TestApplyEnvOverrides_Proxy(t *testing.T) {
	t.Setenv("MCP_HTTPS_PROXY", "http://env-proxy:8080")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	httpProxy := ""
	headers := flagList{}

	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if httpProxy != "http://env-proxy:8080" {
		t.Errorf("Expected proxy from env, got '%s'", httpProxy)
	}
}

func TestApplyEnvOverrides_ProxyCLITakesPrecedence(t *testing.T) {
	t.Setenv("MCP_HTTPS_PROXY", "http://env-proxy:8080")

	serverURL := ""
	port := 3334
	allowHTTP := false
	transport := "auto"
	httpProxy := "http://cli-proxy:3128"
	headers := flagList{}

	applyEnvOverrides(&serverURL, &port, &allowHTTP, &transport, &httpProxy, &headers)

	if httpProxy != "http://cli-proxy:3128" {
		t.Errorf("CLI proxy should take precedence, got '%s'", httpProxy)
	}
}
