package utils

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/naotama2002/mcp-remote-go/internal/transport"
)

// ParsedArgs holds the result of command line argument parsing
type ParsedArgs struct {
	ServerURL         string
	CallbackPort      int
	Headers           map[string]string
	TransportStrategy transport.TransportStrategy
}

// ParseCommandLineArgs parses command line arguments
func ParseCommandLineArgs(args []string, usage string) (*ParsedArgs, error) {
	if len(args) < 1 {
		return nil, errors.New(usage)
	}

	serverURL := args[0]
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		return nil, fmt.Errorf("server URL must start with http:// or https://: %s", serverURL)
	}

	// Set default values
	result := &ParsedArgs{
		ServerURL:         serverURL,
		CallbackPort:      0, // 0 means automatic selection
		Headers:           make(map[string]string),
		TransportStrategy: transport.HTTPFirst,
	}

	// Parse remaining arguments
	i := 1
	for i < len(args) {
		arg := args[i]

		// Callback port
		if i == 1 && !strings.HasPrefix(arg, "-") {
			port, err := strconv.Atoi(arg)
			if err != nil {
				return nil, fmt.Errorf("callback port must be a number: %s", arg)
			}
			result.CallbackPort = port
			i++
			continue
		}

		// Option arguments
		switch arg {
		case "--header", "-H":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--header option requires a value")
			}
			headerValue := args[i+1]
			parts := strings.SplitN(headerValue, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("header must be in 'Name: Value' format: %s", headerValue)
			}
			headerName := strings.TrimSpace(parts[0])
			headerValue = strings.TrimSpace(parts[1])
			result.Headers[headerName] = headerValue
			i += 2

		case "--transport":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--transport option requires a value")
			}
			strategy := args[i+1]
			switch strategy {
			case "sse-only":
				result.TransportStrategy = transport.SSEOnly
			case "http-only":
				result.TransportStrategy = transport.HTTPOnly
			case "sse-first":
				result.TransportStrategy = transport.SSEFirst
			case "http-first":
				result.TransportStrategy = transport.HTTPFirst
			default:
				return nil, fmt.Errorf("invalid transport strategy: %s", strategy)
			}
			i += 2
			


		default:
			return nil, fmt.Errorf("invalid option: %s", arg)
		}
	}

	return result, nil
}
