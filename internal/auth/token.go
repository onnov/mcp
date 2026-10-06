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
		oauthError(w, 405, "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	// Use POST body only; query-string credentials are never accepted.
	f := r.PostForm
	id, secret := f.Get("client_id"), f.Get("client_secret")
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if secret != "" {
			oauthError(w, 400, "invalid_request")
			return
		}
		decodedID, e1 := url.QueryUnescape(basicID)
		decodedSecret, e2 := url.QueryUnescape(basicSecret)
		if e1 != nil || e2 != nil || (id != "" && id != decodedID) {
			oauthError(w, 400, "invalid_request")
			return
		}
		id, secret = decodedID, decodedSecret
	}
	if id != a.ClientID || !equalSecret(secret, a.ClientSecret) {
		oauthError(w, 401, "invalid_client")
		return
	}
	if f.Get("resource") != a.ResourceURL {
		oauthError(w, 400, "invalid_target")
		return
	}
	if scope := f.Get("scope"); scope != "" && scope != "github" {
		oauthError(w, 400, "invalid_scope")
		return
	}
	var grant *oauthGrant
	oldRefresh := ""
	switch f.Get("grant_type") {
	case "authorization_code":
		verifier := f.Get("code_verifier")
		if !verifierPattern.MatchString(verifier) || f.Get("redirect_uri") != a.RedirectURI {
			oauthError(w, 400, "invalid_grant")
			return
		}
		key := tokenHash(f.Get("code"))
		a.mu.Lock()
		a.prune()
		code, ok := a.codes[key]
		if !ok || !equalSecret(code.Challenge, challenge(verifier)) {
			a.mu.Unlock()
			oauthError(w, 400, "invalid_grant")
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
			oauthError(w, 400, "invalid_grant")
			return
		}
	default:
		oauthError(w, 400, "unsupported_grant_type")
		return
	}
	access, err := randomSecret()
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	refresh, err := randomSecret()
	if err != nil {
		oauthError(w, 500, "server_error")
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
		oauthError(w, 400, "invalid_grant")
		return
	}
	if oldRefresh != "" {
		key := tokenHash(oldRefresh)
		if a.refresh[key] != grant {
			a.mu.Unlock()
			oauthError(w, 400, "invalid_grant")
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
		oauthError(w, 429, "temporarily_unavailable")
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
