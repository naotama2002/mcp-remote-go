package errors

import (
	"fmt"
	"net/http"
)

// ErrorType represents different categories of errors
type ErrorType string

const (
	// AuthenticationError represents authentication failures
	AuthenticationError ErrorType = "authentication_error"
	// AuthorizationError represents authorization failures
	AuthorizationError ErrorType = "authorization_error"
	// NetworkError represents network-related failures
	NetworkError ErrorType = "network_error"
	// ConfigurationError represents configuration problems
	ConfigurationError ErrorType = "configuration_error"
	// ValidationError represents input validation failures
	ValidationError ErrorType = "validation_error"
	// ServerError represents server-side failures
	ServerError ErrorType = "server_error"
	// TimeoutError represents timeout failures
	TimeoutError ErrorType = "timeout_error"
)

// AppError represents a structured application error
type AppError struct {
	Type       ErrorType `json:"type"`
	Message    string    `json:"message"`
	Details    string    `json:"details,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	Cause      error     `json:"-"`
}

// Error implements the error interface
func (e *AppError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Type, e.Message, e.Details)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap returns the underlying error
func (e *AppError) Unwrap() error {
	return e.Cause
}

// New creates a new AppError
func New(errorType ErrorType, message string) *AppError {
	return &AppError{
		Type:    errorType,
		Message: message,
	}
}

// Wrap wraps an existing error with additional context
func Wrap(err error, errorType ErrorType, message string) *AppError {
	return &AppError{
		Type:    errorType,
		Message: message,
		Cause:   err,
	}
}

// WithDetails adds details to an AppError
func (e *AppError) WithDetails(details string) *AppError {
	e.Details = details
	return e
}

// WithStatusCode adds an HTTP status code to an AppError
func (e *AppError) WithStatusCode(code int) *AppError {
	e.StatusCode = code
	return e
}

// IsType checks if an error is of a specific type
func IsType(err error, errorType ErrorType) bool {
	var appErr *AppError
	if As(err, &appErr) {
		return appErr.Type == errorType
	}
	return false
}

// As is a convenience wrapper around errors.As
func As(err error, target interface{}) bool {
	switch target := target.(type) {
	case **AppError:
		if appErr, ok := err.(*AppError); ok {
			*target = appErr
			return true
		}
		// Check if it's wrapped in another error
		for err != nil {
			if appErr, ok := err.(*AppError); ok {
				*target = appErr
				return true
			}
			// Try to unwrap
			unwrapper, ok := err.(interface{ Unwrap() error })
			if !ok {
				break
			}
			err = unwrapper.Unwrap()
		}
	}
	return false
}

// Convenience constructors for common error types

// NewAuthenticationError creates an authentication error
func NewAuthenticationError(message string) *AppError {
	return New(AuthenticationError, message).WithStatusCode(http.StatusUnauthorized)
}

// NewAuthorizationError creates an authorization error
func NewAuthorizationError(message string) *AppError {
	return New(AuthorizationError, message).WithStatusCode(http.StatusForbidden)
}

// NewNetworkError creates a network error
func NewNetworkError(message string) *AppError {
	return New(NetworkError, message).WithStatusCode(http.StatusServiceUnavailable)
}

// NewConfigurationError creates a configuration error
func NewConfigurationError(message string) *AppError {
	return New(ConfigurationError, message).WithStatusCode(http.StatusInternalServerError)
}

// NewValidationError creates a validation error
func NewValidationError(message string) *AppError {
	return New(ValidationError, message).WithStatusCode(http.StatusBadRequest)
}

// NewServerError creates a server error
func NewServerError(message string) *AppError {
	return New(ServerError, message).WithStatusCode(http.StatusInternalServerError)
}

// NewTimeoutError creates a timeout error
func NewTimeoutError(message string) *AppError {
	return New(TimeoutError, message).WithStatusCode(http.StatusRequestTimeout)
}

// FromHTTPStatus creates an AppError from an HTTP status code
func FromHTTPStatus(statusCode int, message string) *AppError {
	var errorType ErrorType

	switch {
	case statusCode == http.StatusUnauthorized:
		errorType = AuthenticationError
	case statusCode == http.StatusForbidden:
		errorType = AuthorizationError
	case statusCode >= 400 && statusCode < 500:
		errorType = ValidationError
	case statusCode >= 500:
		errorType = ServerError
	default:
		errorType = NetworkError
	}

	return New(errorType, message).WithStatusCode(statusCode)
}
