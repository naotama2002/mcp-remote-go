package auth

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	// Maximum validity period for lock files (30 minutes)
	maxLockAge = 30 * 60 * 1000
)

// LazyAuthCoordinator is an authentication coordinator that is initialized lazily
type LazyAuthCoordinator struct {
	serverURLHash string
	callbackPort  int
	authState     *AuthState
	mu            sync.Mutex
}

// NewLazyAuthCoordinator creates a new lazily initialized authentication coordinator
func NewLazyAuthCoordinator(serverURLHash string, callbackPort int) *LazyAuthCoordinator {
	return &LazyAuthCoordinator{
		serverURLHash: serverURLHash,
		callbackPort:  callbackPort,
	}
}

// InitializeAuth initializes authentication
func (c *LazyAuthCoordinator) InitializeAuth() (*AuthState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return existing state if authentication is already initialized
	if c.authState != nil {
		return c.authState, nil
	}

	log.Println("Initializing authentication coordination on demand")

	// Coordinate with existing authentication process
	authState, err := CoordinateAuth(c.serverURLHash, c.callbackPort)
	if err != nil {
		return nil, err
	}

	c.authState = authState
	return c.authState, nil
}

// IsPidRunning checks if the process with the specified PID is running
func IsPidRunning(pid int) bool {
	// Always return true on Windows (for simplification)
	if os.PathSeparator == '\\' {
		return true
	}

	// On Unix-like OS, check if the process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Send signal 0 to check process existence
	err = process.Signal(os.Signal(nil))
	return err == nil
}

// IsLockValid checks if the lock file is valid
func IsLockValid(lockData *LockfileData) bool {
	// Lock file is invalid if it's too old
	if time.Now().UnixNano()/int64(time.Millisecond)-lockData.Timestamp > maxLockAge {
		log.Println("Lock file is too old")
		return false
	}

	// Check if the process is running
	if !IsPidRunning(lockData.PID) {
		log.Println("Process in lock file is not running")
		return false
	}

	// Check if endpoint is accessible
	client := &http.Client{
		Timeout: 1 * time.Second,
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/wait-for-auth?poll=false", lockData.Port))
	if err != nil {
		log.Printf("Error connecting to auth server: %v", err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted
}

// WaitForAuthentication waits for authentication from other server instances
func WaitForAuthentication(port int) bool {
	log.Printf("Waiting for authentication from server on port %d...", port)

	for {
		url := fmt.Sprintf("http://127.0.0.1:%d/wait-for-auth", port)
		log.Printf("Query: %s", url)
		
		client := &http.Client{
			Timeout: 10 * time.Second,
		}
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("Error waiting for authentication: %v", err)
			return false
		}

		if resp.StatusCode == http.StatusOK {
			// Authentication completed
			log.Println("Authentication completed by other instance")
			resp.Body.Close()
			return true
		} else if resp.StatusCode == http.StatusAccepted {
			// Continue polling
			log.Println("Authentication is still in progress")
			resp.Body.Close()
			time.Sleep(1 * time.Second)
		} else {
			log.Printf("Unexpected response status: %d", resp.StatusCode)
			resp.Body.Close()
			return false
		}
	}
}

// SetupOAuthCallbackServer sets up the OAuth callback server
func SetupOAuthCallbackServer(port int, path string) (*http.Server, func() (string, error), chan string) {
	authCodeChan := make(chan string, 1)
	var authCode string
	var authCodeSet bool
	var mu sync.Mutex

	mux := http.NewServeMux()
	
	// OAuth callback handler
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Code not found", http.StatusBadRequest)
			return
		}

		mu.Lock()
		authCode = code
		authCodeSet = true
		mu.Unlock()

		// Send to channel asynchronously
		go func() {
			authCodeChan <- code
		}()

		// Show success message to user
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`
			<html>
			<head>
				<title>Authentication Successful</title>
				<style>
					body { font-family: Arial, sans-serif; text-align: center; padding: 50px; }
					.success { color: green; }
				</style>
			</head>
			<body>
				<h1 class="success">Authentication Successful!</h1>
				<p>You can now close this window and return to the application.</p>
			</body>
			</html>
		`))
	})

	// Authentication wait endpoint
	mux.HandleFunc("/wait-for-auth", func(w http.ResponseWriter, r *http.Request) {
		poll := r.URL.Query().Get("poll") != "false"

		mu.Lock()
		codeSet := authCodeSet
		mu.Unlock()

		if codeSet {
			// Authentication completed
			w.WriteHeader(http.StatusOK)
			return
		} else if poll {
			// Continue polling
			w.WriteHeader(http.StatusAccepted)
			return
		} else {
			// Add Authorization header if auth token exists
			w.WriteHeader(http.StatusAccepted)
			return
		}
	})

	// Create server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	// Start server
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// Function to get authentication code and exchange it for tokens
	waitForAuthCode := func() (string, error) {
		mu.Lock()
		if authCodeSet {
			code := authCode
			mu.Unlock()
			return code, nil
		}
		mu.Unlock()

		// Wait for authentication code from channel
		code := <-authCodeChan
		return code, nil
	}

	return server, waitForAuthCode, authCodeChan
}

// CoordinateAuth coordinates authentication between multiple instances
func CoordinateAuth(serverURLHash string, callbackPort int) (*AuthState, error) {
	// Don't use lock files on Windows
	var lockData *LockfileData
	var err error
	
	if os.PathSeparator != '\\' {
		lockData, err = CheckLockfile(serverURLHash)
		if err != nil {
			return nil, fmt.Errorf("failed to check lock file: %w", err)
		}
	}

	// If there's a valid lock file, use existing authentication process
	if lockData != nil && IsLockValid(lockData) {
		log.Printf("Another instance is handling authentication on port %d", lockData.Port)

		// Wait for authentication to complete
		authCompleted := WaitForAuthentication(lockData.Port)
		if authCompleted {
			log.Println("Authentication completed by other instance")

			// Set up dummy server
			mux := http.NewServeMux()
			server := &http.Server{
				Addr:    ":0", // Listen on an available port
				Handler: mux,
			}

			// Dummy function that should not be called
			dummyWaitForAuthCode := func() (string, error) {
				log.Println("Warning: waitForAuthCode was called on a secondary instance - this is unexpected")
				// Return an unresolved promise
				ch := make(chan string)
				return <-ch, nil
			}

			return &AuthState{
				Server:         server,
				WaitForAuthCode: dummyWaitForAuthCode,
				SkipBrowserAuth: true,
			}, nil
		} else {
			log.Println("Taking over authentication process...")
		}

		// If other process did not complete authentication successfully
		if err := DeleteLockfile(serverURLHash); err != nil {
			return nil, fmt.Errorf("failed to delete lock file: %w", err)
		}
	} else if lockData != nil {
		// Delete invalid lock file
		log.Println("Deleting invalid lock file")
		if err := DeleteLockfile(serverURLHash); err != nil {
			return nil, fmt.Errorf("failed to delete lock file: %w", err)
		}
	}

	// Create our own lock file
	server, waitForAuthCode, _ := SetupOAuthCallbackServer(callbackPort, "/oauth/callback")

	// Get the actual port where the server is running
	actualPort := callbackPort
	
	log.Printf("Creating lock file for server %s with process %d on port %d", serverURLHash, os.Getpid(), actualPort)
	if err := CreateLockfile(serverURLHash, os.Getpid(), actualPort); err != nil {
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Delete lock file when process ends
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		log.Printf("Cleaning up lock file for server %s", serverURLHash)
		if err := DeleteLockfile(serverURLHash); err != nil {
			log.Printf("Error cleaning up lock file: %v", err)
		}
	}()

	return &AuthState{
		Server:         server,
		WaitForAuthCode: waitForAuthCode,
		SkipBrowserAuth: false,
	}, nil
}
