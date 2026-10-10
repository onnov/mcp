// Package ownerauth provides single-owner OAuth for the private PC server.
// It is not an identity provider or a multi-user account system.
package ownerauth

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const Scope = "pc"

// ClaudeRedirectURIs are the hosted Claude callbacks (claude.ai web, Desktop,
// mobile). They are accepted alongside the configured ChatGPT callback, so one
// owner client works with both. Each code stays bound to its own callback.
var ClaudeRedirectURIs = []string{"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback"}

type Config struct {
	PublicURL, ClientID, ClientSecret, RedirectURI, PasswordHash string
	// AuthMode is password (default) or client-secret for a private, single-owner client.
	AuthMode       string
	TrustedProxies []netip.Prefix
	// StateDir persists grants across restarts; empty keeps them in memory only.
	StateDir string
}

func (c Config) Validate() error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return errors.New("PC_MCP_PUBLIC_URL must be an HTTPS origin without a path")
	}
	u, err = url.Parse(c.RedirectURI)
	if err != nil || u.Scheme != "https" || u.Host != "chatgpt.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "/connector_platform_oauth_redirect" && (!strings.HasPrefix(u.Path, "/connector/oauth/") || len(strings.TrimPrefix(u.Path, "/connector/oauth/")) == 0 || strings.Contains(strings.TrimPrefix(u.Path, "/connector/oauth/"), "/"))) {
		return errors.New("PC_MCP_OAUTH_REDIRECT_URI must be the exact HTTPS ChatGPT OAuth callback")
	}
	if c.ClientID == "" || len(c.ClientID) > 128 || len(c.ClientSecret) < 32 || len(c.ClientSecret) > 4096 {
		return errors.New("HTTP/SSH requires PC_MCP_OAUTH_CLIENT_ID and PC_MCP_OAUTH_CLIENT_SECRET (32+ characters)")
	}
	if c.AuthMode != "" && c.AuthMode != "password" && c.AuthMode != "client-secret" {
		return errors.New("owner-auth must be password or client-secret")
	}
	if c.AuthMode == "client-secret" {
		return nil // Confidential-client authentication remains mandatory at /oauth/token.
	}
	cost, err := bcrypt.Cost([]byte(c.PasswordHash))
	if err != nil || cost < 10 || cost > 14 || len(c.PasswordHash) != 60 {
		return errors.New("PC_MCP_OWNER_PASSWORD_HASH must be a bcrypt hash with cost 10..14; use pc-mcp --hash-password")
	}
	return nil
}

func HashPassword(password []byte) ([]byte, error) {
	if len(password) < 16 || len(password) > 72 {
		return nil, errors.New("owner password must be 16..72 bytes")
	}
	return bcrypt.GenerateFromPassword(password, 12)
}

type pending struct {
	State, Challenge, RedirectURI string
	Cookie                        [32]byte
	Expires                       time.Time
}
type code struct {
	Challenge, RedirectURI string
	Expires                time.Time
}
type grant struct {
	ID, Client string
	Expires    time.Time
	used       [][32]byte // recent rotated refresh hashes, for replay detection
}
type access struct {
	Grant   *grant
	Expires time.Time
}
type Server struct {
	Config
	mu             sync.Mutex
	consumed       map[[32]byte]time.Time
	envelope       cipher.AEAD
	loginLimit     *Limiter
	authorizeLimit *Limiter
	anonymousLimit *Limiter
	codes          map[[32]byte]code
	access         map[[32]byte]access
	refresh        map[[32]byte]*grant
	usedRefresh    map[[32]byte]*grant
	login          chan struct{}
}

func New(c Config) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	aead, err := newEnvelope()
	if err != nil {
		return nil, err
	}
	s := &Server{Config: c, envelope: aead, consumed: map[[32]byte]time.Time{}, loginLimit: &Limiter{Max: 10, Period: time.Minute}, authorizeLimit: &Limiter{Max: 30, Period: time.Minute}, anonymousLimit: &Limiter{Max: 60, Period: time.Minute}, codes: map[[32]byte]code{}, access: map[[32]byte]access{}, refresh: map[[32]byte]*grant{}, usedRefresh: map[[32]byte]*grant{}, login: make(chan struct{}, 2)}
	if err := s.loadGrants(); err != nil {
		return nil, err
	}
	return s, nil
}
func hash(s string) [32]byte { return sha256.Sum256([]byte(s)) }
func same(a, b string) bool {
	x, y := hash(a), hash(b)
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func challenge(s string) string {
	x := hash(s)
	return base64.RawURLEncoding.EncodeToString(x[:])
}
func random() string { return base64.RawURLEncoding.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 32)
	// crypto/rand.Read is guaranteed to fill the buffer or terminate on Go 1.27.
	_, _ = rand.Read(b)
	return b
}
func jsonResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func oauthError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

// oauthFail explains a rejection to the browser or client and in the server
// log, so the owner can see which step failed. Descriptions never hold secrets.
func oauthFail(w http.ResponseWriter, r *http.Request, status int, code, description string) {
	log.Printf("pc-mcp: OAuth %s %s rejected (%d %s): %s", r.Method, r.URL.Path, status, code, description)
	jsonResponse(w, status, map[string]string{"error": code, "error_description": description})
}
func (s *Server) resource() string { return s.PublicURL + "/mcp" }
func (s *Server) allowedRedirect(uri string) bool {
	return uri == s.RedirectURI || slices.Contains(ClaudeRedirectURIs, uri)
}
func (s *Server) prune() {
	now := time.Now()
	for k, expiry := range s.consumed {
		if !now.Before(expiry) {
			delete(s.consumed, k)
		}
	}
	for k, c := range s.codes {
		if !now.Before(c.Expires) {
			delete(s.codes, k)
		}
	}
	for k, a := range s.access {
		if !now.Before(a.Expires) || !now.Before(a.Grant.Expires) {
			delete(s.access, k)
		}
	}
	for k, g := range s.refresh {
		if !now.Before(g.Expires) {
			delete(s.refresh, k)
			s.deleteGrantFile(g)
		}
	}
	for k, g := range s.usedRefresh {
		if !now.Before(g.Expires) {
			delete(s.usedRefresh, k)
		}
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	metadata := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthFail(w, r, 405, "invalid_request", "metadata accepts only GET")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonResponse(w, 200, map[string]any{"resource": s.resource(), "authorization_servers": []string{s.PublicURL}, "scopes_supported": []string{Scope}})
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", metadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", metadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			oauthFail(w, r, 405, "invalid_request", "metadata accepts only GET")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		jsonResponse(w, 200, map[string]any{
			"issuer": s.PublicURL, "authorization_endpoint": s.PublicURL + "/oauth/authorize", "token_endpoint": s.PublicURL + "/oauth/token", "revocation_endpoint": s.PublicURL + "/oauth/revoke",
			"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{Scope}, "authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("/oauth/authorize", s.authorize)
	mux.HandleFunc("/oauth/login", s.ownerLogin)
	mux.HandleFunc("/oauth/token", s.token)
	mux.HandleFunc("/oauth/revoke", s.revoke)
}

func (s *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		ok, client := false, ""
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			s.mu.Lock()
			a, exists := s.access[hash(parts[1])]
			now := time.Now()
			ok = exists && now.Before(a.Expires) && now.Before(a.Grant.Expires) && s.grantAlive(a.Grant)
			if ok {
				client = a.Grant.Client
			}
			s.mu.Unlock()
		}
		if !ok {
			if !s.anonymousLimit.Allow(ClientIP(r, s.TrustedProxies)) {
				w.Header().Set("Retry-After", "60")
				oauthError(w, 429, "temporarily_unavailable")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.PublicURL+`/.well-known/oauth-protected-resource", scope="pc"`)
			oauthError(w, 401, "invalid_token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientContextKey{}, client)))
	})
}

type clientContextKey struct{}

// ClientFromContext names the authenticated chat client (chatgpt or claude).
func ClientFromContext(ctx context.Context) string {
	client, _ := ctx.Value(clientContextKey{}).(string)
	return client
}
