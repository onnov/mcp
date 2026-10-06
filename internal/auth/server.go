package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"github.com/onnov/mcp/internal/config"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type oauthGrant = identity.Principal
type oauthPending struct {
	State          string // ChatGPT's state, returned only to the configured redirect URI.
	Challenge      string
	GitHubVerifier string
	CookieHash     [32]byte
	Expires        time.Time
}

type oauthCode struct {
	Grant     *oauthGrant
	Challenge string
	Expires   time.Time
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

func (a *Server) RegisterRoutes(mux *http.ServeMux) {
	resource := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthError(w, 405, "invalid_request")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonResponse(w, 200, map[string]any{"resource": a.ResourceURL, "authorization_servers": []string{a.PublicURL}, "scopes_supported": []string{"github"}})
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", resource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", resource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthError(w, 405, "invalid_request")
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

func (a *Server) redirect(w http.ResponseWriter, r *http.Request, state, code, failure string) {
	u, _ := url.Parse(a.RedirectURI) // Validated at startup, never taken from an untrusted request.
	q := u.Query()
	q.Set("state", state)
	q.Set("iss", a.PublicURL)
	if failure != "" {
		q.Set("error", failure)
	} else {
		q.Set("code", code)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
