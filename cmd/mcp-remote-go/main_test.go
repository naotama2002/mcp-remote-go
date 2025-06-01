package main

import (
	"testing"
)

func TestGetServerURLHash(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		expected  string
	}{
		{
			name:      "https URL",
			serverURL: "https://example.com",
			expected:  "4f5b812789fc606be1b3b16908db13fc7a9adf8ca72a1fdd6d91b1d769b56ba8",
		},
		{
			name:      "http URL",
			serverURL: "http://localhost:8080",
			expected:  "bb3c84cb5b5df4133c3cf711fcc3ab51eb8e3b9a49b8b0b8d97e39b1a2e7b8b1",
		},
		{
			name:      "empty string",
			serverURL: "",
			expected:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getServerURLHash(tt.serverURL)
			if len(result) != 64 {
				t.Errorf("Expected hash length 64, got %d", len(result))
			}
			// Verify hash is unique
			if result == "" {
				t.Error("Hash should not be empty")
			}
		})
	}

	// Verify same input produces same hash
	hash1 := getServerURLHash("https://example.com")
	hash2 := getServerURLHash("https://example.com")
	if hash1 != hash2 {
		t.Errorf("Same input should produce same hash, got %s and %s", hash1, hash2)
	}

	// Verify different inputs produce different hashes
	hash3 := getServerURLHash("https://different.com")
	if hash1 == hash3 {
		t.Error("Different inputs should produce different hashes")
	}
}

func TestFlagList(t *testing.T) {
	fl := &flagList{}

	// Test String method with empty list
	if fl.String() != "[]" {
		t.Errorf("Expected '[]', got '%s'", fl.String())
	}

	// Test Set method
	err := fl.Set("Authorization:Bearer token")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(*fl) != 1 {
		t.Errorf("Expected length 1, got %d", len(*fl))
	}

	if (*fl)[0] != "Authorization:Bearer token" {
		t.Errorf("Expected 'Authorization:Bearer token', got '%s'", (*fl)[0])
	}

	// Test multiple Set calls
	err = fl.Set("Content-Type:application/json")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(*fl) != 2 {
		t.Errorf("Expected length 2, got %d", len(*fl))
	}

	if (*fl)[1] != "Content-Type:application/json" {
		t.Errorf("Expected 'Content-Type:application/json', got '%s'", (*fl)[1])
	}

	// Test String method with values
	result := fl.String()
	expected := "[Authorization:Bearer token Content-Type:application/json]"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}
