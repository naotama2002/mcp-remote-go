package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config holds HTTP client configuration
type Config struct {
	Timeout        time.Duration
	MaxRetries     int
	RetryDelay     time.Duration
	DefaultHeaders map[string]string
}

// DefaultConfig returns a default HTTP client configuration
func DefaultConfig() *Config {
	return &Config{
		Timeout:        30 * time.Second,
		MaxRetries:     3,
		RetryDelay:     time.Second,
		DefaultHeaders: make(map[string]string),
	}
}

// Client wraps http.Client with common functionality
type Client struct {
	httpClient *http.Client
	config     *Config
}

// New creates a new HTTP client with the given configuration
func New(config *Config) *Client {
	if config == nil {
		config = DefaultConfig()
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		config: config,
	}
}

// Request represents an HTTP request
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    interface{}
}

// Response represents an HTTP response with convenience methods
type Response struct {
	*http.Response
	BodyBytes []byte
}

// SafeClose safely closes the response body with error logging
func (r *Response) SafeClose() error {
	if r.Response == nil || r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

// JSON unmarshals the response body into the provided interface
func (r *Response) JSON(v interface{}) error {
	if len(r.BodyBytes) == 0 {
		return fmt.Errorf("empty response body")
	}
	return json.Unmarshal(r.BodyBytes, v)
}

// String returns the response body as a string
func (r *Response) String() string {
	return string(r.BodyBytes)
}

// Do performs an HTTP request with retries and proper error handling
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	var lastErr error

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.config.RetryDelay):
			}
		}

		resp, err := c.doSingle(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// Don't retry on context cancellation or client errors (4xx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return resp, err
		}
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", c.config.MaxRetries+1, lastErr)
}

// doSingle performs a single HTTP request
func (c *Client) doSingle(ctx context.Context, req *Request) (*Response, error) {
	// Prepare request body
	var bodyReader io.Reader
	if req.Body != nil {
		switch body := req.Body.(type) {
		case string:
			bodyReader = bytes.NewBufferString(body)
		case []byte:
			bodyReader = bytes.NewBuffer(body)
		case io.Reader:
			bodyReader = body
		default:
			// JSON marshal for other types
			jsonBytes, err := json.Marshal(req.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewBuffer(jsonBytes)
		}
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set default headers
	for key, value := range c.config.DefaultHeaders {
		httpReq.Header.Set(key, value)
	}

	// Set request-specific headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// Perform request
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	// Read response body
	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		_ = httpResp.Body.Close()
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Create response wrapper
	resp := &Response{
		Response:  httpResp,
		BodyBytes: bodyBytes,
	}

	// Check for HTTP errors
	if httpResp.StatusCode >= 400 {
		err = fmt.Errorf("HTTP %d - %s", httpResp.StatusCode, string(bodyBytes))
	}

	return resp, err
}

// Get performs a GET request
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) (*Response, error) {
	return c.Do(ctx, &Request{
		Method:  http.MethodGet,
		URL:     url,
		Headers: headers,
	})
}

// Post performs a POST request
func (c *Client) Post(ctx context.Context, url string, body interface{}, headers map[string]string) (*Response, error) {
	if headers == nil {
		headers = make(map[string]string)
	}

	// Set content type for JSON if not specified
	if _, exists := headers["Content-Type"]; !exists {
		switch b := body.(type) {
		case string:
			if len(b) > 0 && b[0] == '{' {
				headers["Content-Type"] = "application/json"
			}
		default:
			headers["Content-Type"] = "application/json"
		}
	}

	return c.Do(ctx, &Request{
		Method:  http.MethodPost,
		URL:     url,
		Headers: headers,
		Body:    body,
	})
}

// PostForm performs a POST request with form data
func (c *Client) PostForm(ctx context.Context, url string, formData map[string]string, headers map[string]string) (*Response, error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	// Convert form data to URL-encoded string
	values := make([]string, 0, len(formData))
	for key, value := range formData {
		values = append(values, fmt.Sprintf("%s=%s", key, value))
	}
	body := ""
	for i, v := range values {
		if i > 0 {
			body += "&"
		}
		body += v
	}

	return c.Do(ctx, &Request{
		Method:  http.MethodPost,
		URL:     url,
		Headers: headers,
		Body:    body,
	})
}
