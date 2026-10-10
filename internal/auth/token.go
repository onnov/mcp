package auth

import (
	"github.com/onnov/mcp/internal/identity"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (a *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthFail(w, r, 405, "invalid_request", "token endpoint accepts only POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthFail(w, r, 400, "invalid_request", "cannot parse token form")
		return
	}
	// Use POST body only; query-string credentials are never accepted.
	f := r.PostForm
	id, secret := f.Get("client_id"), f.Get("client_secret")
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if secret != "" {
			oauthFail(w, r, 400, "invalid_request", "client_secret is sent both in the Authorization header and in the form")
			return
		}
		decodedID, e1 := url.QueryUnescape(basicID)
		decodedSecret, e2 := url.QueryUnescape(basicSecret)
		if e1 != nil || e2 != nil || (id != "" && id != decodedID) {
			oauthFail(w, r, 400, "invalid_request", "conflicting client_id in the Authorization header and form")
			return
		}
		id, secret = decodedID, decodedSecret
	}
	if id != a.ClientID || !equalSecret(secret, a.ClientSecret) {
		oauthFail(w, r, 401, "invalid_client", "client_id or client_secret does not match MCP_CLIENT_ID / MCP_CLIENT_SECRET")
		return
	}
	if f.Get("resource") != a.ResourceURL {
		oauthFail(w, r, 400, "invalid_target", "resource must be "+a.ResourceURL)
		return
	}
	if scope := f.Get("scope"); scope != "" && scope != "github" {
		oauthFail(w, r, 400, "invalid_scope", "scope must be github")
		return
	}
	var grant *oauthGrant
	oldRefresh := ""
	switch f.Get("grant_type") {
	case "authorization_code":
		verifier := f.Get("code_verifier")
		key := tokenHash(f.Get("code"))
		a.mu.Lock()
		a.prune()
		code, ok := a.codes[key]
		// The code is bound to the callback of its own authorization request.
		if !ok || !verifierPattern.MatchString(verifier) || !equalSecret(code.Challenge, challenge(verifier)) || f.Get("redirect_uri") != code.RedirectURI {
			a.mu.Unlock()
			oauthFail(w, r, 400, "invalid_grant", "authorization code is unknown, expired (1 minute) or used, or code_verifier/redirect_uri does not match its authorization request")
			return
		}
		delete(a.codes, key)
		grant = code.Grant
		a.mu.Unlock()
	case "refresh_token":
		oldRefresh = f.Get("refresh_token")
		a.mu.Lock()
		a.prune()
		grant = a.refresh[tokenHash(oldRefresh)]
		a.mu.Unlock()
		if grant == nil {
			oauthFail(w, r, 400, "invalid_grant", "refresh token is unknown, rotated or expired (24 hours, or after a server restart); connect again")
			return
		}
	default:
		oauthFail(w, r, 400, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}
	access, err := randomSecret()
	if err != nil {
		oauthFail(w, r, 500, "server_error", "cannot generate token")
		return
	}
	refresh, err := randomSecret()
	if err != nil {
		oauthFail(w, r, 500, "server_error", "cannot generate token")
		return
	}
	expires := time.Now().Add(time.Hour)
	if grant.Expires.Before(expires) {
		expires = grant.Expires
	}
	a.mu.Lock()
	a.prune()
	if !time.Now().Before(grant.Expires) {
		a.mu.Unlock()
		oauthFail(w, r, 400, "invalid_grant", "GitHub authorization expired (24 hours); connect again")
		return
	}
	if oldRefresh != "" {
		key := tokenHash(oldRefresh)
		if a.refresh[key] != grant {
			a.mu.Unlock()
			oauthFail(w, r, 400, "invalid_grant", "refresh token was already used")
			return
		}
		delete(a.refresh, key)
		// Rotate the whole MCP token pair, keeping the upstream GitHub token internal.
		for k, v := range a.access {
			if v.Grant == grant {
				delete(a.access, k)
			}
		}
	}
	if len(a.access) >= 100 || len(a.refresh) >= 100 {
		a.mu.Unlock()
		oauthFail(w, r, 429, "temporarily_unavailable", "too many active connections")
		return
	}
	a.access[tokenHash(access)] = oauthAccess{Grant: grant, Expires: expires}
	a.refresh[tokenHash(refresh)] = grant
	a.mu.Unlock()
	seconds := int(time.Until(expires) / time.Second)
	jsonResponse(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": seconds, "scope": "github"})
}

func (a *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		var session oauthAccess
		ok := false
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			a.mu.Lock()
			a.prune()
			session, ok = a.access[tokenHash(parts[1])]
			a.mu.Unlock()
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+a.PublicURL+`/.well-known/oauth-protected-resource", scope="github"`)
			oauthError(w, http.StatusUnauthorized, "invalid_token")
			return
		}
		ctx := identity.With(r.Context(), session.Grant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
