package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/onnov/mcp/internal/config"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
)

// ClaudeRedirectURIs are the hosted Claude callbacks (claude.ai web, Desktop,
// mobile). They are accepted alongside the configured ChatGPT callback, so one
// MCP client works with both. Each authorization request and its code stay
// bound to the callback that started it.
var ClaudeRedirectURIs = []string{"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback"}

type oauthGrant = identity.Principal
type oauthPending struct {
	State          string // The chat client's state, returned only to RedirectURI.
	RedirectURI    string // Validated callback of this authorization request.
	Challenge      string
	GitHubVerifier string
	CookieHash     [32]byte
	Expires        time.Time
}

type oauthCode struct {
	Grant       *oauthGrant
	Challenge   string
	RedirectURI string
	Expires     time.Time
}

type oauthAccess struct {
	Grant   *oauthGrant
	Expires time.Time
}

type Server struct {
	PublicURL          string
	ResourceURL        string
	GitHubClientID     string
	GitHubClientSecret string
	ClientID           string
	ClientSecret       string
	RedirectURI        string
	AllowedUsers       map[string]bool
	AllowedUserID      int64
	GitHubScopes       string
	GitHub             *github.Client
	mu                 sync.Mutex
	pending            map[[32]byte]oauthPending
	codes              map[[32]byte]oauthCode
	access             map[[32]byte]oauthAccess
	refresh            map[[32]byte]*oauthGrant
}

func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func tokenHash(s string) [32]byte  { return sha256.Sum256([]byte(s)) }
func equalSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func challenge(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func New(c config.Config, g *github.Client) *Server {
	return &Server{
		PublicURL: c.PublicURL, ResourceURL: c.PublicURL + "/mcp",
		GitHubClientID: c.GitHubClientID, GitHubClientSecret: c.GitHubClientSecret,
		ClientID: c.MCPClientID, ClientSecret: c.MCPClientSecret, RedirectURI: c.RedirectURI,
		AllowedUsers: c.AllowedUsers, AllowedUserID: c.AllowedUserID, GitHubScopes: c.GitHubScopes, GitHub: g,
		pending: map[[32]byte]oauthPending{}, codes: map[[32]byte]oauthCode{}, access: map[[32]byte]oauthAccess{}, refresh: map[[32]byte]*oauthGrant{},
	}
}

// Called only under a.mu. All maps are bounded and expired credentials are removed.
func (a *Server) prune() {
	now := time.Now()
	for k, v := range a.pending {
		if !now.Before(v.Expires) {
			delete(a.pending, k)
		}
	}
	for k, v := range a.codes {
		if !now.Before(v.Expires) || !now.Before(v.Grant.Expires) {
			delete(a.codes, k)
		}
	}
	for k, v := range a.access {
		if !now.Before(v.Expires) || !now.Before(v.Grant.Expires) {
			delete(a.access, k)
		}
	}
	for k, v := range a.refresh {
		if !now.Before(v.Expires) {
			delete(a.refresh, k)
		}
	}
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	jsonResponse(w, status, map[string]string{"error": code})
}

// oauthFail explains a rejection to the browser or client and in the server
// log, so the operator can see which step failed. Descriptions never hold secrets.
func oauthFail(w http.ResponseWriter, r *http.Request, status int, code, description string) {
	log.Printf("github-mcp: OAuth %s %s rejected (%d %s): %s", r.Method, r.URL.Path, status, code, description)
	jsonResponse(w, status, map[string]string{"error": code, "error_description": description})
}

func (a *Server) allowedRedirect(uri string) bool {
	return uri == a.RedirectURI || slices.Contains(ClaudeRedirectURIs, uri)
}

// clientKey names the chat client by its validated callback.
func clientKey(redirectURI string) string {
	if u, err := url.Parse(redirectURI); err == nil && u.Host == "chatgpt.com" {
		return "chatgpt"
	}
	return "claude"
}

func (a *Server) RegisterRoutes(mux *http.ServeMux) {
	resource := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthFail(w, r, 405, "invalid_request", "metadata accepts only GET")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonResponse(w, 200, map[string]any{"resource": a.ResourceURL, "authorization_servers": []string{a.PublicURL}, "scopes_supported": []string{"github"}})
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", resource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", resource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthFail(w, r, 405, "invalid_request", "metadata accepts only GET")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonResponse(w, 200, map[string]any{
			"issuer": a.PublicURL, "authorization_endpoint": a.PublicURL + "/oauth/authorize", "token_endpoint": a.PublicURL + "/oauth/token",
			"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
			"code_challenge_methods_supported":      []string{"S256"}, "scopes_supported": []string{"github"},
			"authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("/oauth/authorize", a.authorize)
	mux.HandleFunc("/oauth/github/callback", a.githubCallback)
	mux.HandleFunc("/oauth/token", a.token)
}

// redirect returns to the callback bound to the authorization request; it was
// checked against the allowed callbacks before any state was stored.
func (a *Server) redirect(w http.ResponseWriter, r *http.Request, redirectURI, state, code, failure string) {
	a.redirectWith(w, r, redirectURI, state, code, failure, "")
}

func (a *Server) redirectWith(w http.ResponseWriter, r *http.Request, redirectURI, state, code, failure, description string) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("state", state)
	q.Set("iss", a.PublicURL)
	if failure != "" {
		q.Set("error", failure)
		if description != "" {
			q.Set("error_description", description)
		}
	} else {
		q.Set("code", code)
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}
