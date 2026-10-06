package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/config"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/ownerauth"
	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/tools"
	"github.com/onnov/mcp/internal/pc/workspace"
	"golang.org/x/crypto/bcrypt"
)

type authenticatedHTTP struct {
	token string
	base  http.RoundTripper
}

func (a authenticatedHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = "pc.example.com"
	if a.token != "" {
		r.Header.Set("Authorization", "Bearer "+a.token)
	}
	return a.base.RoundTrip(r)
}

func TestPCAuthenticatedHTTPWithoutGitHub(t *testing.T) {
	const password = "a-private-PC-owner-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	root, state := filepath.Join(d, "root"), filepath.Join(d, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	engine := &sandbox.Engine{MaxSeconds: 10}
	ws, err := workspace.New(root, state, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jm := jobs.New(ctx, ws, engine, 10)
	defer jm.Close()
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()
	cfg := config.Config{HTTPAddr: srv.Listener.Addr().String(), OAuth: ownerauth.Config{PublicURL: "https://pc.example.com", ClientID: "pc-chatgpt", ClientSecret: strings.Repeat("s", 32), PasswordHash: string(hash), RedirectURI: "https://chatgpt.com/connector_platform_oauth_redirect"}}
	handler, err := httpHandler(cfg, tools.New(ws, jm, tools.Options{OAuth: true}))
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = handler
	srv.Start()
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: authenticatedHTTP{base: base}, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	if session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: client}, nil); err == nil {
		session.Close()
		t.Fatal("anonymous MCP access accepted")
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {cfg.OAuth.ClientID}, "redirect_uri": {cfg.OAuth.RedirectURI}, "response_type": {"code"}, "resource": {cfg.OAuth.PublicURL + "/mcp"}, "state": {"test-state"}, "scope": {"pc"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}}
	response, err := client.Get(srv.URL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	nonce := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(nonce) != 2 || response.StatusCode != 200 {
		t.Fatal("owner login unavailable", string(body))
	}
	// The outer middleware's no-referrer default must be overridden for this
	// document: a browser otherwise sends Origin: null on its HTML form POST.
	if response.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatal("outer HTTP handler broke the browser login Origin")
	}
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "form-action 'self' "+cfg.OAuth.RedirectURI+";") {
		t.Fatal("browser login CSP blocks the return to ChatGPT")
	}
	login := url.Values{"request": {nonce[1]}, "password": {password}, "action": {"allow"}}
	for _, origin := range []string{"null", "", "https://evil.example"} {
		r, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/oauth/login", strings.NewReader(login.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.AddCookie(response.Cookies()[0])
		denied, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		denied.Body.Close()
		if denied.StatusCode != 403 {
			t.Fatal("unsafe login origin accepted through HTTP middleware", origin)
		}
	}
	r, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/oauth/login", strings.NewReader(login.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", cfg.OAuth.PublicURL)
	r.AddCookie(response.Cookies()[0])
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	location, _ := url.Parse(response.Header.Get("Location"))
	if response.StatusCode != 303 || location.Query().Get("code") == "" {
		t.Fatal("owner consent failed", response.StatusCode)
	}
	f := url.Values{"client_id": {cfg.OAuth.ClientID}, "client_secret": {cfg.OAuth.ClientSecret}, "grant_type": {"authorization_code"}, "resource": {cfg.OAuth.PublicURL + "/mcp"}, "redirect_uri": {cfg.OAuth.RedirectURI}, "code": {location.Query().Get("code")}, "code_verifier": {verifier}}
	response, err = client.PostForm(srv.URL+"/oauth/token", f)
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		Access string `json:"access_token"`
	}
	err = json.NewDecoder(response.Body).Decode(&token)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || token.Access == "" {
		t.Fatal("token exchange failed", err, response.StatusCode)
	}
	client.Transport = authenticatedHTTP{token: token.Access, base: base}
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 16 {
		t.Fatal("HTTP tool discovery failed", err)
	}
	for _, tool := range list.Tools {
		meta, _ := json.Marshal(tool.Meta["securitySchemes"])
		if !strings.Contains(string(meta), `"oauth2"`) || strings.Contains(string(meta), `"noauth"`) {
			t.Fatal("HTTP tool missing OAuth policy", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pc_write_file", Arguments: map[string]any{"directory": ".", "branch": "", "path": "hello.txt", "text": "owner HTTP edit", "expected_revision": "new"}})
	if err != nil || result.IsError {
		t.Fatal("authenticated file write failed", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(content) != "owner HTTP edit" {
		t.Fatal("write did not reach local workspace", err)
	}
	f = url.Values{"client_id": {cfg.OAuth.ClientID}, "client_secret": {cfg.OAuth.ClientSecret}, "token": {token.Access}}
	response, err = client.PostForm(srv.URL+"/oauth/revoke", f)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err = session.ListTools(ctx, nil); err == nil {
		t.Fatal("revoked token still accesses MCP")
	}
	bad, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	bad.Header.Set("Origin", "https://evil.example")
	response, err = client.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin request accepted")
	}
	bad, _ = http.NewRequest("GET", srv.URL+"/healthz", nil)
	bad.Host = "evil.example"
	response, err = (&http.Client{Transport: base, Timeout: time.Second}).Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("unknown Host bypassed outer DNS rebinding protection")
	}
}
