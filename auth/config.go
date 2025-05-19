package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version はmcp-remoteのバージョンです
const Version = "0.1.0"

// LockfileData はロックファイルのデータ構造を定義します
type LockfileData struct {
	PID       int   `json:"pid"`
	Port      int   `json:"port"`
	Timestamp int64 `json:"timestamp"`
}

// GetConfigDir は設定ディレクトリのパスを取得します
func GetConfigDir() string {
	baseConfigDir := os.Getenv("MCP_REMOTE_CONFIG_DIR")
	if baseConfigDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			// エラーが発生した場合は現在のディレクトリを使用
			return filepath.Join(".", ".mcp-auth")
		}
		baseConfigDir = filepath.Join(homeDir, ".mcp-auth")
	}
	// バージョンサブディレクトリを追加
	return filepath.Join(baseConfigDir, fmt.Sprintf("mcp-remote-go-%s", Version))
}

// EnsureConfigDir は設定ディレクトリが存在することを確認します
func EnsureConfigDir() error {
	configDir := GetConfigDir()
	return os.MkdirAll(configDir, 0700)
}

// GetConfigFilePath は設定ファイルのパスを取得します
func GetConfigFilePath(serverURLHash string, filename string) string {
	configDir := GetConfigDir()
	return filepath.Join(configDir, fmt.Sprintf("%s_%s", serverURLHash, filename))
}

// DeleteConfigFile は設定ファイルを削除します
func DeleteConfigFile(serverURLHash string, filename string) error {
	filePath := GetConfigFilePath(serverURLHash, filename)
	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete %s: %w", filename, err)
	}
	return nil
}

// ReadJSONFile はJSONファイルを読み込み、指定された構造体にパースします
func ReadJSONFile(serverURLHash string, filename string, v interface{}) error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", filename, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to parse %s: %w", filename, err)
	}

	return nil
}

// WriteJSONFile はデータをJSONファイルに書き込みます
func WriteJSONFile(serverURLHash string, filename string, v interface{}) error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", filename, err)
	}

	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", filename, err)
	}

	return nil
}

// ReadTextFile はテキストファイルを読み込みます
func ReadTextFile(serverURLHash string, filename string, errorMessage string) (string, error) {
	if err := EnsureConfigDir(); err != nil {
		return "", err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", errorMessage, err)
	}

	return string(data), nil
}

// WriteTextFile はテキストをファイルに書き込みます
func WriteTextFile(serverURLHash string, filename string, text string) error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	if err := os.WriteFile(filePath, []byte(text), 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", filename, err)
	}

	return nil
}

// CreateLockfile はロックファイルを作成します
func CreateLockfile(serverURLHash string, pid int, port int) error {
	lockData := LockfileData{
		PID:       pid,
		Port:      port,
		Timestamp: time.Now().UnixMilli(),
	}
	return WriteJSONFile(serverURLHash, "lock.json", &lockData)
}

// CheckLockfile はロックファイルをチェックします
func CheckLockfile(serverURLHash string) (*LockfileData, error) {
	var lockData LockfileData
	err := ReadJSONFile(serverURLHash, "lock.json", &lockData)
	if err != nil {
		return nil, err
	}
	return &lockData, nil
}

// DeleteLockfile はロックファイルを削除します
func DeleteLockfile(serverURLHash string) error {
	return DeleteConfigFile(serverURLHash, "lock.json")
}
