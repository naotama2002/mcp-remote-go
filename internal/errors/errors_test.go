package errors

import (
	"fmt"
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	err := New(AuthenticationError, "test message")

	if err.Type != AuthenticationError {
		t.Errorf("Expected type %s, got %s", AuthenticationError, err.Type)
	}

	if err.Message != "test message" {
		t.Errorf("Expected message 'test message', got %s", err.Message)
	}

	expected := "authentication_error: test message"
	if err.Error() != expected {
		t.Errorf("Expected error string '%s', got '%s'", expected, err.Error())
	}
}

func TestWrap(t *testing.T) {
	originalErr := fmt.Errorf("original error")
	wrappedErr := Wrap(originalErr, NetworkError, "network failed")

	if wrappedErr.Type != NetworkError {
		t.Errorf("Expected type %s, got %s", NetworkError, wrappedErr.Type)
	}

	if wrappedErr.Unwrap() != originalErr {
		t.Error("Wrapped error should unwrap to original error")
	}
}

func TestWithDetails(t *testing.T) {
	err := New(ValidationError, "invalid input").WithDetails("field 'name' is required")

	expected := "validation_error: invalid input (field 'name' is required)"
	if err.Error() != expected {
		t.Errorf("Expected error string '%s', got '%s'", expected, err.Error())
	}
}

func TestWithStatusCode(t *testing.T) {
	err := New(ServerError, "internal error").WithStatusCode(http.StatusInternalServerError)

	if err.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code %d, got %d", http.StatusInternalServerError, err.StatusCode)
	}
}

func TestIsType(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		errType  ErrorType
		expected bool
	}{
		{
			name:     "matching type",
			err:      New(AuthenticationError, "auth failed"),
			errType:  AuthenticationError,
			expected: true,
		},
		{
			name:     "different type",
			err:      New(AuthenticationError, "auth failed"),
			errType:  NetworkError,
			expected: false,
		},
		{
			name:     "non-app error",
			err:      fmt.Errorf("regular error"),
			errType:  AuthenticationError,
			expected: false,
		},
		{
			name:     "wrapped app error",
			err:      fmt.Errorf("wrapper: %w", New(ValidationError, "validation failed")),
			errType:  ValidationError,
			expected: true, // Our As implementation should handle fmt.Errorf wrapping via Unwrap
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsType(tt.err, tt.errType)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestAs(t *testing.T) {
	appErr := New(AuthenticationError, "auth failed")

	var targetErr *AppError
	if !As(appErr, &targetErr) {
		t.Error("As should return true for AppError")
	}

	if targetErr != appErr {
		t.Error("As should set target to the original error")
	}

	// Test with non-AppError
	regularErr := fmt.Errorf("regular error")
	var targetErr2 *AppError
	if As(regularErr, &targetErr2) {
		t.Error("As should return false for non-AppError")
	}
}

func TestConvenienceConstructors(t *testing.T) {
	tests := []struct {
		name           string
		constructor    func(string) *AppError
		expectedType   ErrorType
		expectedStatus int
	}{
		{
			name:           "authentication error",
			constructor:    NewAuthenticationError,
			expectedType:   AuthenticationError,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "authorization error",
			constructor:    NewAuthorizationError,
			expectedType:   AuthorizationError,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "network error",
			constructor:    NewNetworkError,
			expectedType:   NetworkError,
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "configuration error",
			constructor:    NewConfigurationError,
			expectedType:   ConfigurationError,
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "validation error",
			constructor:    NewValidationError,
			expectedType:   ValidationError,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "server error",
			constructor:    NewServerError,
			expectedType:   ServerError,
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:           "timeout error",
			constructor:    NewTimeoutError,
			expectedType:   TimeoutError,
			expectedStatus: http.StatusRequestTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.constructor("test message")

			if err.Type != tt.expectedType {
				t.Errorf("Expected type %s, got %s", tt.expectedType, err.Type)
			}

			if err.StatusCode != tt.expectedStatus {
				t.Errorf("Expected status code %d, got %d", tt.expectedStatus, err.StatusCode)
			}

			if err.Message != "test message" {
				t.Errorf("Expected message 'test message', got %s", err.Message)
			}
		})
	}
}

func TestFromHTTPStatus(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		expectedType ErrorType
	}{
		{
			name:         "401 unauthorized",
			statusCode:   http.StatusUnauthorized,
			expectedType: AuthenticationError,
		},
		{
			name:         "403 forbidden",
			statusCode:   http.StatusForbidden,
			expectedType: AuthorizationError,
		},
		{
			name:         "400 bad request",
			statusCode:   http.StatusBadRequest,
			expectedType: ValidationError,
		},
		{
			name:         "404 not found",
			statusCode:   http.StatusNotFound,
			expectedType: ValidationError,
		},
		{
			name:         "500 internal server error",
			statusCode:   http.StatusInternalServerError,
			expectedType: ServerError,
		},
		{
			name:         "502 bad gateway",
			statusCode:   http.StatusBadGateway,
			expectedType: ServerError,
		},
		{
			name:         "200 ok (edge case)",
			statusCode:   http.StatusOK,
			expectedType: NetworkError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FromHTTPStatus(tt.statusCode, "test message")

			if err.Type != tt.expectedType {
				t.Errorf("Expected type %s, got %s", tt.expectedType, err.Type)
			}

			if err.StatusCode != tt.statusCode {
				t.Errorf("Expected status code %d, got %d", tt.statusCode, err.StatusCode)
			}
		})
	}
}

func TestErrorChaining(t *testing.T) {
	// Test that we can chain errors properly
	originalErr := fmt.Errorf("database connection failed")
	appErr := Wrap(originalErr, NetworkError, "failed to connect to server")
	finalErr := Wrap(appErr, AuthenticationError, "authentication service unavailable")

	// Should be able to unwrap to the previous error
	if finalErr.Unwrap() != appErr {
		t.Error("Should unwrap to immediate parent error")
	}

	// Check that the final error has the right type
	if finalErr.Type != AuthenticationError {
		t.Errorf("Expected final error type %s, got %s", AuthenticationError, finalErr.Type)
	}
}
