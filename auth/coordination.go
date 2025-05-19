package auth

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"
)

// AuthCoordinator は、認証コーディネーターのインターフェースです
type AuthCoordinator interface {
	InitializeAuth() (*AuthState, error)
}

// AuthState は、認証状態を表します
type AuthState struct {
	Server         *http.Server
	WaitForAuthCode func() (string, error)
	SkipBrowserAuth bool
}

// LazyAuthCoordinator は、必要に応じて認証を初期化する認証コーディネーターです
type LazyAuthCoordinator struct {
	serverURLHash string
	callbackPort  int
	authState     *AuthState
	mu            sync.Mutex
}

// NewLazyAuthCoordinator は、新しい LazyAuthCoordinator インスタンスを作成します
func NewLazyAuthCoordinator(serverURLHash string, callbackPort int) *LazyAuthCoordinator {
	return &LazyAuthCoordinator{
		serverURLHash: serverURLHash,
		callbackPort:  callbackPort,
	}
}

// InitializeAuth は、認証を初期化します
func (c *LazyAuthCoordinator) InitializeAuth() (*AuthState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 既に認証が初期化されている場合は、既存の状態を返す
	if c.authState != nil {
		return c.authState, nil
	}

	log.Println("Initializing auth coordination on-demand")

	// 既存の認証プロセスを調整
	authState, err := coordinateAuth(c.serverURLHash, c.callbackPort)
	if err != nil {
		return nil, err
	}

	c.authState = authState
	return authState, nil
}

// IsLockValid は、ロックファイルが有効かどうかを確認します
func IsLockValid(lockData *LockfileData) bool {
	// ロックファイルが古すぎる場合（30分以上）
	const maxLockAge = 30 * 60 * 1000 // 30分
	if time.Now().UnixMilli()-lockData.Timestamp > maxLockAge {
		log.Println("Lockfile is too old")
		return false
	}

	// プロセスがまだ実行中かどうか確認
	if !IsPidRunning(lockData.PID) {
		log.Println("Process from lockfile is not running")
		return false
	}

	// エンドポイントがアクセス可能かどうか確認
	client := &http.Client{
		Timeout: time.Second,
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/wait-for-auth?poll=false", lockData.Port))
	if err != nil {
		log.Printf("Error connecting to auth server: %v", err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted
}

// WaitForAuthentication は、別のサーバーインスタンスからの認証を待ちます
func WaitForAuthentication(port int) bool {
	log.Printf("Waiting for authentication from the server on port %d...", port)

	client := &http.Client{
		Timeout: 0, // タイムアウトなし
	}

	for {
		url := fmt.Sprintf("http://127.0.0.1:%d/wait-for-auth", port)
		log.Printf("Querying: %s", url)
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("Error waiting for authentication: %v", err)
			return false
		}

		if resp.StatusCode == http.StatusOK {
			// 認証完了
			resp.Body.Close()
			log.Println("Authentication completed by other instance")
			return true
		} else if resp.StatusCode == http.StatusAccepted {
			// ポーリング継続
			resp.Body.Close()
			log.Println("Authentication still in progress")
			time.Sleep(time.Second)
		} else {
			resp.Body.Close()
			log.Printf("Unexpected response status: %d", resp.StatusCode)
			return false
		}
	}
}

// coordinateAuth は、複数のクライアント/プロキシインスタンス間で認証を調整します
func coordinateAuth(serverURLHash string, callbackPort int) (*AuthState, error) {
	// ロックファイルをチェック（Windowsでは一時的に無効）
	var lockData *LockfileData
	var err error
	if runtime.GOOS != "windows" {
		lockData, err = CheckLockfile(serverURLHash)
		if err != nil {
			log.Printf("Error checking lockfile: %v", err)
		}
	}

	// 有効なロックファイルがある場合、既存の認証プロセスを使用
	if lockData != nil && IsLockValid(lockData) {
		log.Printf("Another instance is handling authentication on port %d", lockData.Port)

		// 認証が完了するのを待つ
		authCompleted := WaitForAuthentication(lockData.Port)
		if authCompleted {
			log.Println("Authentication completed by another instance")

			// ダミーサーバーをセットアップ - クライアントはディスクから直接トークンを使用
			dummyServer := &http.Server{}

			// API互換性のためのダミー関数
			dummyWaitForAuthCode := func() (string, error) {
				log.Println("WARNING: waitForAuthCode called in secondary instance - this is unexpected")
				// 解決しないPromiseを返す - クライアントはディスクからトークンを使用するはず
				return "", fmt.Errorf("unexpected call to waitForAuthCode in secondary instance")
			}

			return &AuthState{
				Server:         dummyServer,
				WaitForAuthCode: dummyWaitForAuthCode,
				SkipBrowserAuth: true,
			}, nil
		} else {
			log.Println("Taking over authentication process...")
		}
	} else if lockData != nil {
		// 無効なロックファイル、削除
		log.Println("Found invalid lockfile, deleting it")
		if err := DeleteLockfile(serverURLHash); err != nil {
			log.Printf("Error deleting lockfile: %v", err)
		}
	}

	// OAuthコールバックサーバーをセットアップ
	server, waitForAuthCode, err := SetupOAuthCallbackServer(callbackPort, "/oauth/callback")
	if err != nil {
		return nil, fmt.Errorf("failed to setup OAuth callback server: %w", err)
	}

	// サーバーが実行されているポートを取得
	actualPort := server.Addr[len(":"):]

	log.Printf("Creating lockfile for server %s with process %d on port %s", serverURLHash, GetCurrentPID(), actualPort)
	if err := CreateLockfile(serverURLHash, GetCurrentPID(), callbackPort, time.Now().UnixMilli()); err != nil {
		log.Printf("Error creating lockfile: %v", err)
	}

	// プロセス終了時にロックファイルを削除
	go func() {
		<-context.Background().Done()
		log.Printf("Cleaning up lockfile for server %s", serverURLHash)
		if err := DeleteLockfile(serverURLHash); err != nil {
			log.Printf("Error cleaning up lockfile: %v", err)
		}
	}()

	return &AuthState{
		Server:         server,
		WaitForAuthCode: waitForAuthCode,
		SkipBrowserAuth: false,
	}, nil
}

// GetCurrentPID は、現在のプロセスIDを取得します
func GetCurrentPID() int {
	return os.Getpid()
}
