package auth

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// AuthState は認証状態を表す構造体です
type AuthState struct {
	Server         *http.Server
	WaitForAuthCode func() (string, error)
	SkipBrowserAuth bool
}

// AuthCoordinator は認証調整を行うインターフェースです
type AuthCoordinator interface {
	InitializeAuth() (*AuthState, error)
}

// LazyAuthCoordinator は必要に応じて認証を初期化する調整機能を提供します
type LazyAuthCoordinator struct {
	serverURLHash string
	callbackPort  int
	authState     *AuthState
}

// NewLazyAuthCoordinator は新しい LazyAuthCoordinator を作成します
func NewLazyAuthCoordinator(serverURLHash string, callbackPort int) *LazyAuthCoordinator {
	return &LazyAuthCoordinator{
		serverURLHash: serverURLHash,
		callbackPort:  callbackPort,
	}
}

// InitializeAuth は認証を初期化します
func (c *LazyAuthCoordinator) InitializeAuth() (*AuthState, error) {
	// 既に初期化されている場合は既存の状態を返す
	if c.authState != nil {
		return c.authState, nil
	}

	log.Println("Initializing auth coordination on-demand")

	// 認証調整を実行
	authState, err := CoordinateAuth(c.serverURLHash, c.callbackPort)
	if err != nil {
		return nil, err
	}

	c.authState = authState
	return authState, nil
}

// IsPidRunning は指定されたPIDのプロセスが実行中かどうかを確認します
func IsPidRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Windowsの場合は常にプロセスが見つかるため、シグナル0を送信して確認
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// IsLockValid はロックファイルが有効かどうかを確認します
func IsLockValid(lockData *LockfileData) bool {
	// ロックが古すぎる場合（30分以上）
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

// WaitForAuthentication は他のサーバーインスタンスからの認証を待ちます
func WaitForAuthentication(port int) bool {
	log.Printf("Waiting for authentication from the server on port %d...", port)

	for {
		url := fmt.Sprintf("http://127.0.0.1:%d/wait-for-auth", port)
		log.Printf("Querying: %s", url)
		
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Error waiting for authentication: %v", err)
			return false
		}
		
		if resp.StatusCode == http.StatusOK {
			// 認証完了
			log.Println("Authentication completed by other instance")
			resp.Body.Close()
			return true
		} else if resp.StatusCode == http.StatusAccepted {
			// ポーリング継続
			log.Println("Authentication still in progress")
			resp.Body.Close()
			time.Sleep(time.Second)
		} else {
			log.Printf("Unexpected response status: %d", resp.StatusCode)
			resp.Body.Close()
			return false
		}
	}
}

// CoordinateAuth は複数のクライアント/プロキシインスタンス間で認証を調整します
func CoordinateAuth(serverURLHash string, callbackPort int) (*AuthState, error) {
	// ロックファイルをチェック（Windowsの場合は無効）
	var lockData *LockfileData
	var err error
	
	if os.Getenv("GOOS") != "windows" {
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
			dummyServer := &http.Server{Addr: ":0"}

			// 通常の操作では呼び出されないはずのダミー関数を提供
			dummyWaitForAuthCode := func() (string, error) {
				log.Println("WARNING: waitForAuthCode called in secondary instance - this is unexpected")
				// 解決しないプロミスを返す - クライアントはディスクからトークンを使用するはず
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

		// 他のプロセスが認証を正常に完了しなかった場合
		err := DeleteLockfile(serverURLHash)
		if err != nil {
			log.Printf("Error deleting lockfile: %v", err)
		}
	} else if lockData != nil {
		// 無効なロックファイル、削除
		log.Println("Found invalid lockfile, deleting it")
		err := DeleteLockfile(serverURLHash)
		if err != nil {
			log.Printf("Error deleting lockfile: %v", err)
		}
	}

	// 独自のロックファイルを作成
	authCodeChan := make(chan string, 1)
	server, actualPort := SetupOAuthCallbackServerWithLongPoll(callbackPort, "/oauth/callback", authCodeChan)

	log.Printf("Creating lockfile for server %s with process %d on port %d", serverURLHash, os.Getpid(), actualPort)
	err = CreateLockfile(serverURLHash, os.Getpid(), actualPort)
	if err != nil {
		log.Printf("Error creating lockfile: %v", err)
	}

	// プロセス終了時にロックファイルを削除
	cleanupHandler := func() {
		log.Printf("Cleaning up lockfile for server %s", serverURLHash)
		err := DeleteLockfile(serverURLHash)
		if err != nil {
			log.Printf("Error cleaning up lockfile: %v", err)
		}
	}
	
	// シグナルハンドラーを設定
	SetupSignalHandlers(cleanupHandler)

	waitForAuthCode := func() (string, error) {
		code := <-authCodeChan
		return code, nil
	}

	return &AuthState{
		Server:         server,
		WaitForAuthCode: waitForAuthCode,
		SkipBrowserAuth: false,
	}, nil
}

// SetupOAuthCallbackServerWithLongPoll はOAuthコールバックサーバーをセットアップします
func SetupOAuthCallbackServerWithLongPoll(port int, path string, authCodeChan chan<- string) (*http.Server, int) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	var authCode string

	// ロングポーリングエンドポイント
	router.GET("/wait-for-auth", func(c *gin.Context) {
		if authCode != "" {
			// 認証が既に完了している - コードなしで200を返す
			// セカンダリインスタンスはディスクからトークンを読み取る
			log.Println("Auth already completed, returning 200")
			c.Status(http.StatusOK)
			return
		}

		if c.Query("poll") == "false" {
			log.Println("Client requested no long poll, responding with 202")
			c.Status(http.StatusAccepted)
			return
		}

		// ロングポール - 最大30秒待機
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		select {
		case <-ctx.Done():
			log.Println("Long poll timeout reached, responding with 202")
			c.Status(http.StatusAccepted)
		case <-time.After(100 * time.Millisecond):
			// authCodeが設定されたかどうかを定期的にチェック
			if authCode != "" {
				log.Println("Auth completed during long poll, responding with 200")
				c.Status(http.StatusOK)
				return
			}
			c.Status(http.StatusAccepted)
		}
	})

	// OAuthコールバックエンドポイント
	router.GET(path, func(c *gin.Context) {
		code := c.Query("code")
		if code == "" {
			c.String(http.StatusBadRequest, "Error: No authorization code received")
			return
		}

		authCode = code
		log.Println("Auth code received")
		
		// 認証コードをチャネルに送信
		select {
		case authCodeChan <- code:
			log.Println("Auth code sent to channel")
		default:
			log.Println("Warning: Could not send auth code to channel")
		}

		c.Header("Content-Type", "text/html")
		c.String(http.StatusOK, `
			Authorization successful!
			You may close this window and return to the CLI.
			<script>
				// If this is a non-interactive session (no manual approval step was required) then 
				// this should automatically close the window. If not, this will have no effect and 
				// the user will see the message above.
				window.close();
			</script>
		`)
	})

	// 利用可能なポートでサーバーを起動
	server := &http.Server{
		Handler: router,
	}

	// 指定されたポートが0の場合、利用可能なポートを見つける
	actualPort := port
	if port == 0 {
		actualPort = FindAvailablePort(0)
	}

	server.Addr = fmt.Sprintf(":%d", actualPort)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Error starting OAuth callback server: %v", err)
		}
	}()

	log.Printf("OAuth callback server running at http://127.0.0.1:%d", actualPort)
	return server, actualPort
}

// SetupSignalHandlers はシグナルハンドラーをセットアップします
func SetupSignalHandlers(cleanup func()) {
	// 実際の実装ではos/signalパッケージを使用してシグナルハンドラーを設定
	// この簡略化された実装では、プロセス終了時にcleanup関数が呼び出されるようにする
}

// FindAvailablePort は利用可能なポートを見つけます
func FindAvailablePort(preferredPort int) int {
	// 優先ポートが指定されている場合、それを試す
	if preferredPort > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", preferredPort))
		if err == nil {
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			return port
		}
	}

	// 利用可能なポートを見つける
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		// エラーが発生した場合はデフォルトポートを返す
		return 3000
	}
	
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}
