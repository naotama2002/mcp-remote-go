package auth

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const (
	// MCPRemoteVersion は、MCP-Remote のバージョンです
	MCPRemoteVersion = "0.1.0"
)

// LockfileData は、ロックファイルのデータ構造を表します
type LockfileData struct {
	PID       int   `json:"pid"`
	Port      int   `json:"port"`
	Timestamp int64 `json:"timestamp"`
}

var (
	configDirMutex sync.Mutex
	configDirCache string
)

// GetConfigDir は、設定ディレクトリのパスを取得します
func GetConfigDir() string {
	configDirMutex.Lock()
	defer configDirMutex.Unlock()

	if configDirCache != "" {
		return configDirCache
	}

	// 環境変数から設定ディレクトリを取得するか、デフォルトを使用
	baseConfigDir := os.Getenv("MCP_REMOTE_CONFIG_DIR")
	if baseConfigDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			// ホームディレクトリが取得できない場合は、一時ディレクトリを使用
			baseConfigDir = os.TempDir()
		} else {
			baseConfigDir = filepath.Join(homeDir, ".mcp-auth")
		}
	}

	// バージョンサブディレクトリを追加
	configDirCache = filepath.Join(baseConfigDir, fmt.Sprintf("mcp-remote-go-%s", MCPRemoteVersion))
	return configDirCache
}

// EnsureConfigDir は、設定ディレクトリが存在することを確認します
func EnsureConfigDir() error {
	configDir := GetConfigDir()
	return os.MkdirAll(configDir, 0700)
}

// GetConfigFilePath は、設定ファイルのパスを取得します
func GetConfigFilePath(serverURLHash, filename string) string {
	configDir := GetConfigDir()
	return filepath.Join(configDir, fmt.Sprintf("%s_%s", serverURLHash, filename))
}

// DeleteConfigFile は、設定ファイルを削除します
func DeleteConfigFile(serverURLHash, filename string) error {
	filePath := GetConfigFilePath(serverURLHash, filename)
	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error deleting %s: %w", filename, err)
	}
	return nil
}

// ReadJSONFile は、JSONファイルを読み込み、指定された型にパースします
func ReadJSONFile[T any](serverURLHash, filename string) (*T, error) {
	if err := EnsureConfigDir(); err != nil {
		return nil, err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error reading %s: %w", filename, err)
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling %s: %w", filename, err)
	}

	return &result, nil
}

// WriteJSONFile は、指定されたデータをJSONファイルに書き込みます
func WriteJSONFile(serverURLHash, filename string, data interface{}) error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling data for %s: %w", filename, err)
	}

	if err := os.WriteFile(filePath, jsonData, 0600); err != nil {
		return fmt.Errorf("error writing %s: %w", filename, err)
	}

	return nil
}

// ReadTextFile は、テキストファイルを読み込みます
func ReadTextFile(serverURLHash, filename string) (string, error) {
	if err := EnsureConfigDir(); err != nil {
		return "", err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("error reading %s: %w", filename, err)
	}

	return string(data), nil
}

// WriteTextFile は、テキストをファイルに書き込みます
func WriteTextFile(serverURLHash, filename string, text string) error {
	if err := EnsureConfigDir(); err != nil {
		return err
	}

	filePath := GetConfigFilePath(serverURLHash, filename)
	if err := os.WriteFile(filePath, []byte(text), 0600); err != nil {
		return fmt.Errorf("error writing %s: %w", filename, err)
	}

	return nil
}

// CreateLockfile は、指定されたサーバーのロックファイルを作成します
func CreateLockfile(serverURLHash string, pid, port int, timestamp int64) error {
	lockData := LockfileData{
		PID:       pid,
		Port:      port,
		Timestamp: timestamp,
	}
	return WriteJSONFile(serverURLHash, "lock.json", lockData)
}

// CheckLockfile は、指定されたサーバーのロックファイルを確認します
func CheckLockfile(serverURLHash string) (*LockfileData, error) {
	return ReadJSONFile[LockfileData](serverURLHash, "lock.json")
}

// DeleteLockfile は、指定されたサーバーのロックファイルを削除します
func DeleteLockfile(serverURLHash string) error {
	return DeleteConfigFile(serverURLHash, "lock.json")
}

// GetServerURLHash は、サーバーURLのハッシュを生成します
func GetServerURLHash(serverURL string) string {
	hash := sha256.Sum256([]byte(serverURL))
	return fmt.Sprintf("%x", hash[:8]) // 最初の8バイトを16進数文字列として使用
}

// IsPidRunning は、指定されたPIDのプロセスが実行中かどうかを確認します
func IsPidRunning(pid int) bool {
	if runtime.GOOS == "windows" {
		// Windowsでの実装は複雑なので、常にfalseを返す
		return false
	}

	// Unix系OSでは、シグナル0を送信してプロセスの存在を確認
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// シグナル0を送信（実際にはシグナルを送信せず、プロセスの存在確認のみ）
	err = process.Signal(os.Signal(nil))
	return err == nil
}
