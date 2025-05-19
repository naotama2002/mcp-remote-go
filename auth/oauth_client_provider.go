package auth

import (
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"runtime"
)

// OAuthProviderOptions は、OAuth プロバイダーの設定オプションを表します
type OAuthProviderOptions struct {
	ServerURL       string
	CallbackPort    int
	CallbackPath    string
	ClientName      string
	ClientURI       string
	SoftwareID      string
	SoftwareVersion string
}

// GoOAuthClientProvider は、OAuthClientProvider インターフェースの Go 実装です
type GoOAuthClientProvider struct {
	serverURLHash  string
	callbackPath   string
	clientName     string
	clientURI      string
	softwareID     string
	softwareVersion string
	callbackPort   int
	serverURL      string
}

// NewGoOAuthClientProvider は、新しい GoOAuthClientProvider インスタンスを作成します
func NewGoOAuthClientProvider(options OAuthProviderOptions) *GoOAuthClientProvider {
	callbackPath := options.CallbackPath
	if callbackPath == "" {
		callbackPath = "/oauth/callback"
	}

	clientName := options.ClientName
	if clientName == "" {
		clientName = "MCP CLI Client"
	}

	clientURI := options.ClientURI
	if clientURI == "" {
		clientURI = "https://github.com/naotama2002/mcp-remote-go"
	}

	softwareID := options.SoftwareID
	if softwareID == "" {
		softwareID = "2e6dc280-f3c3-4e01-99a7-8181dbd1d23d"
	}

	softwareVersion := options.SoftwareVersion
	if softwareVersion == "" {
		softwareVersion = MCPRemoteVersion
	}

	return &GoOAuthClientProvider{
		serverURLHash:  GetServerURLHash(options.ServerURL),
		callbackPath:   callbackPath,
		clientName:     clientName,
		clientURI:      clientURI,
		softwareID:     softwareID,
		softwareVersion: softwareVersion,
		callbackPort:   options.CallbackPort,
		serverURL:      options.ServerURL,
	}
}

// GetRedirectURL は、リダイレクトURLを取得します
func (p *GoOAuthClientProvider) GetRedirectURL() string {
	return fmt.Sprintf("http://localhost:%d%s", p.callbackPort, p.callbackPath)
}

// GetClientMetadata は、クライアントメタデータを取得します
func (p *GoOAuthClientProvider) GetClientMetadata() map[string]interface{} {
	return map[string]interface{}{
		"redirect_uris":             []string{p.GetRedirectURL()},
		"token_endpoint_auth_method": "none",
		"grant_types":               []string{"authorization_code", "refresh_token"},
		"response_types":            []string{"code"},
		"client_name":               p.clientName,
		"client_uri":                p.clientURI,
		"software_id":               p.softwareID,
		"software_version":          p.softwareVersion,
	}
}

// ClientInformation は、保存されているクライアント情報を取得します
func (p *GoOAuthClientProvider) ClientInformation() (*OAuthClientInformationFull, error) {
	return ReadJSONFile[OAuthClientInformationFull](p.serverURLHash, "client_info.json")
}

// SaveClientInformation は、クライアント情報を保存します
func (p *GoOAuthClientProvider) SaveClientInformation(clientInformation *OAuthClientInformationFull) error {
	return WriteJSONFile(p.serverURLHash, "client_info.json", clientInformation)
}

// Tokens は、保存されているトークンを取得します
func (p *GoOAuthClientProvider) Tokens() (*OAuthTokens, error) {
	return ReadJSONFile[OAuthTokens](p.serverURLHash, "tokens.json")
}

// SaveTokens は、トークンを保存します
func (p *GoOAuthClientProvider) SaveTokens(tokens *OAuthTokens) error {
	return WriteJSONFile(p.serverURLHash, "tokens.json", tokens)
}

// RedirectToAuthorization は、認証URLにユーザーをリダイレクトします
func (p *GoOAuthClientProvider) RedirectToAuthorization(authorizationURL *url.URL) error {
	urlStr := authorizationURL.String()
	log.Printf("\nPlease authorize this client by visiting:\n%s\n", urlStr)

	// ブラウザを開く試み
	err := openBrowser(urlStr)
	if err != nil {
		log.Println("Could not open browser automatically. Please copy and paste the URL above into your browser.")
	} else {
		log.Println("Browser opened automatically.")
	}

	return nil
}

// SaveCodeVerifier は、PKCE コード検証子を保存します
func (p *GoOAuthClientProvider) SaveCodeVerifier(codeVerifier string) error {
	return WriteTextFile(p.serverURLHash, "code_verifier.txt", codeVerifier)
}

// CodeVerifier は、保存されている PKCE コード検証子を取得します
func (p *GoOAuthClientProvider) CodeVerifier() (string, error) {
	codeVerifier, err := ReadTextFile(p.serverURLHash, "code_verifier.txt")
	if err != nil {
		return "", fmt.Errorf("no code verifier saved for session: %w", err)
	}
	return codeVerifier, nil
}

// openBrowser は、指定されたURLをデフォルトブラウザで開きます
func openBrowser(url string) error {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}

	return err
}
