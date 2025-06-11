package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	// Test with nil config
	client := New(nil)
	if client == nil {
		t.Fatal("Expected client to be created with nil config")
		return
	}
	if client.config.Timeout != 30*time.Second {
		t.Errorf("Expected default timeout 30s, got %v", client.config.Timeout)
	}

	// Test with custom config
	config := &Config{
		Timeout:    10 * time.Second,
		MaxRetries: 5,
		RetryDelay: 2 * time.Second,
	}
	client = New(config)
	if client.config.Timeout != 10*time.Second {
		t.Errorf("Expected timeout 10s, got %v", client.config.Timeout)
	}
	if client.config.MaxRetries != 5 {
		t.Errorf("Expected max retries 5, got %d", client.config.MaxRetries)
	}
}

func TestGet(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET method, got %s", r.Method)
		}
		if r.Header.Get("Test-Header") != "test-value" {
			t.Errorf("Expected test header, got %s", r.Header.Get("Test-Header"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"success"}`))
	}))
	defer server.Close()

	client := New(nil)
	headers := map[string]string{
		"Test-Header": "test-value",
	}

	resp, err := client.Get(context.Background(), server.URL, headers)
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	defer func() { _ = resp.SafeClose() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	err = resp.JSON(&result)
	if err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if result["message"] != "success" {
		t.Errorf("Expected message 'success', got %v", result["message"])
	}
}

func TestPost(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected JSON content type, got %s", r.Header.Get("Content-Type"))
		}

		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		bodyStr := string(body[:n])

		if !strings.Contains(bodyStr, "test") {
			t.Errorf("Expected body to contain 'test', got %s", bodyStr)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":123}`))
	}))
	defer server.Close()

	client := New(nil)
	requestBody := map[string]interface{}{
		"name":  "test",
		"value": 42,
	}

	resp, err := client.Post(context.Background(), server.URL, requestBody, nil)
	if err != nil {
		t.Fatalf("POST request failed: %v", err)
	}
	defer func() { _ = resp.SafeClose() }()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", resp.StatusCode)
	}
}

func TestPostForm(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Expected form content type, got %s", r.Header.Get("Content-Type"))
		}

		err := r.ParseForm()
		if err != nil {
			t.Errorf("Failed to parse form: %v", err)
		}

		if r.Form.Get("username") != "testuser" {
			t.Errorf("Expected username 'testuser', got %s", r.Form.Get("username"))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("form received"))
	}))
	defer server.Close()

	client := New(nil)
	formData := map[string]string{
		"username": "testuser",
		"password": "testpass",
	}

	resp, err := client.PostForm(context.Background(), server.URL, formData, nil)
	if err != nil {
		t.Fatalf("POST form request failed: %v", err)
	}
	defer func() { _ = resp.SafeClose() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if resp.String() != "form received" {
		t.Errorf("Expected response 'form received', got %s", resp.String())
	}
}

func TestErrorHandling(t *testing.T) {
	// Test server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := New(nil)
	resp, err := client.Get(context.Background(), server.URL, nil)
	if err == nil {
		t.Error("Expected error for 404 response")
	}
	if resp == nil {
		t.Error("Expected response even with error")
	} else {
		defer func() { _ = resp.SafeClose() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", resp.StatusCode)
		}
	}
}

func TestRetryLogic(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Fail first 2 attempts
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server error"))
		} else {
			// Succeed on 3rd attempt
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
		}
	}))
	defer server.Close()

	config := &Config{
		Timeout:    5 * time.Second,
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond, // Short delay for test
	}
	client := New(config)

	resp, err := client.Get(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatalf("Request should succeed after retries: %v", err)
	}
	defer func() { _ = resp.SafeClose() }()

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestTimeoutHandling(t *testing.T) {
	// Server with delay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Timeout:    50 * time.Millisecond, // Very short timeout
		MaxRetries: 0,
	}
	client := New(config)

	_, err := client.Get(context.Background(), server.URL, nil)
	if err == nil {
		t.Error("Expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Expected timeout-related error, got: %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Get(ctx, server.URL, nil)
	if err == nil {
		t.Error("Expected context cancellation error")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context deadline exceeded, got: %v", err)
	}
}

func TestSafeClose(t *testing.T) {
	resp := &Response{}

	// Test with nil response
	err := resp.SafeClose()
	if err != nil {
		t.Errorf("SafeClose should not error with nil response: %v", err)
	}

	// Test with nil body
	resp.Response = &http.Response{}
	err = resp.SafeClose()
	if err != nil {
		t.Errorf("SafeClose should not error with nil body: %v", err)
	}
}
