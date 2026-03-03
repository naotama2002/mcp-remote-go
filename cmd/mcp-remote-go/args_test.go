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
