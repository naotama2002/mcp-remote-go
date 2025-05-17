package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// MCP configuration directory name
	mcpConfigDirName = ".mcp"
	// Lock file name
	lockFileName = "lock"
	// Client information file name
	clientInfoFileName = "client_info.json"
	// Token file name
	tokensFileName = "tokens.json"
	// Code verifier file name
	codeVerifierFileName = "code_verifier.txt"
)

// GetConfigDir returns the path to the MCP configuration directory
func GetConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, mcpConfigDirName)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}

	return configDir, nil
}

// GetServerConfigDir returns the path to the server-specific configuration directory
func GetServerConfigDir(serverURLHash string) (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}

	serverDir := filepath.Join(configDir, serverURLHash)
	if err := os.MkdirAll(serverDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create server config directory: %w", err)
	}

	return serverDir, nil
}

// GetServerURLHash generates a hash of the server URL
func GetServerURLHash(serverURL string) string {
	hash := sha256.Sum256([]byte(serverURL))
	return hex.EncodeToString(hash[:])[:16]
}

// ReadJSONFile reads a JSON file
func ReadJSONFile(serverURLHash string, fileName string, v interface{}) error {
	serverDir, err := GetServerConfigDir(serverURLHash)
	if err != nil {
		return err
	}

	filePath := filepath.Join(serverDir, fileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read file: %w", err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	return nil
}

// WriteJSONFile writes a JSON file
func WriteJSONFile(serverURLHash string, fileName string, v interface{}) error {
	serverDir, err := GetServerConfigDir(serverURLHash)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	filePath := filepath.Join(serverDir, fileName)
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// ReadTextFile reads a text file
func ReadTextFile(serverURLHash string, fileName string) (string, error) {
	serverDir, err := GetServerConfigDir(serverURLHash)
	if err != nil {
		return "", err
	}

	filePath := filepath.Join(serverDir, fileName)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(data), nil
}

// WriteTextFile writes a text file
func WriteTextFile(serverURLHash string, fileName string, content string) error {
	serverDir, err := GetServerConfigDir(serverURLHash)
	if err != nil {
		return err
	}

	filePath := filepath.Join(serverDir, fileName)
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// CheckLockfile checks the lock file
func CheckLockfile(serverURLHash string) (*LockfileData, error) {
	var lockData LockfileData
	err := ReadJSONFile(serverURLHash, lockFileName, &lockData)
	if err != nil {
		return nil, err
	}

	// Return nil if lock file doesn't exist
	if lockData.PID == 0 {
		return nil, nil
	}

	return &lockData, nil
}

// CreateLockfile creates a lock file
func CreateLockfile(serverURLHash string, pid int, port int) error {
	lockData := LockfileData{
		PID:       pid,
		Port:      port,
		Timestamp: time.Now().UnixNano() / int64(time.Millisecond),
	}

	return WriteJSONFile(serverURLHash, lockFileName, lockData)
}

// DeleteLockfile deletes the lock file
func DeleteLockfile(serverURLHash string) error {
	serverDir, err := GetServerConfigDir(serverURLHash)
	if err != nil {
		return err
	}

	filePath := filepath.Join(serverDir, lockFileName)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to delete lock file: %w", err)
	}

	return nil
}
