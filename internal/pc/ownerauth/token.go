package ownerauth

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func validChallenge(c string) bool {
	b, e := base64.RawURLEncoding.DecodeString(c)
	return e == nil && len(b) == 32 && len(c) == 43
}

func (s *Server) clientForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if r.Method != http.MethodPost {
		oauthFail(w, r, 405, "invalid_request", "token endpoint accepts only POST")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthFail(w, r, 400, "invalid_request", "cannot parse token form")
		return nil, false
	}
	f := r.PostForm // Never accept credentials in query strings.
	id, secret := f.Get("client_id"), f.Get("client_secret")
	if bi, bs, ok := r.BasicAuth(); ok {
		var e1, e2 error
		bi, e1 = url.QueryUnescape(bi)
		bs, e2 = url.QueryUnescape(bs)
		if secret != "" || e1 != nil || e2 != nil || (id != "" && id != bi) {
			oauthFail(w, r, 400, "invalid_request", "conflicting client credentials in Authorization header and form")
			return nil, false
		}
		id, secret = bi, bs
	}
	if id != s.ClientID || !same(secret, s.ClientSecret) {
		oauthFail(w, r, 401, "invalid_client", "client_id or client_secret does not match PC_MCP_OAUTH_CLIENT_ID / PC_MCP_OAUTH_CLIENT_SECRET")
		return nil, false
	}
	return f, true
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	f, ok := s.clientForm(w, r)
	if !ok {
		return
	}
	if f.Get("resource") != s.resource() {
		oauthFail(w, r, 400, "invalid_target", "resource must be "+s.resource()+"")
		return
	}
	if scope := f.Get("scope"); scope != "" && scope != Scope {
		oauthFail(w, r, 400, "invalid_scope", "scope must be "+Scope+"")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	var g *grant
	var consumed [32]byte
	var codeExpires time.Time
	used := [][32]byte{}
	switch f.Get("grant_type") {
	case "authorization_code":
		verifier := f.Get("code_verifier")
		c, exists := s.codes[hash(f.Get("code"))]
		if s.AuthMode == "client-secret" {
			var err error
			c, err = s.openClientCode(f.Get("code"))
			_, used := s.consumed[hash(f.Get("code"))]
			exists = err == nil && !used
			if exists && len(s.consumed) >= 1024 {
				oauthError(w, 429, "temporarily_unavailable")
				return
			}
		}
		if !exists || !verifierPattern.MatchString(verifier) || !same(c.Challenge, challenge(verifier)) || f.Get("redirect_uri") != c.RedirectURI {
			oauthFail(w, r, 400, "invalid_grant", "authorization code is unknown, expired (1 minute) or used, or code_verifier/redirect_uri does not match")
			return
		}
		if len(s.access) >= 100 || len(s.refresh) >= 100 {
			oauthError(w, 429, "temporarily_unavailable")
			return
		}
		consumed = hash(f.Get("code"))
		codeExpires = c.Expires
		g = &grant{ID: newGrantID(), Client: clientKey(c.RedirectURI), Expires: time.Now().Add(7 * 24 * time.Hour)}
	case "refresh_token":
		consumed = hash(f.Get("refresh_token"))
		g = s.refresh[consumed]
		if g == nil {
			if replay := s.usedRefresh[consumed]; replay != nil {
				s.revokeGrant(replay)
			}
			oauthFail(w, r, 400, "invalid_grant", "refresh token is unknown, rotated or revoked; connect again")
			return
		}
		if !s.grantAlive(g) {
			oauthFail(w, r, 400, "invalid_grant", "connection was revoked: its file in the OAuth state directory was deleted")
			return
		}
		if len(s.usedRefresh) >= 1024 {
			oauthError(w, 429, "temporarily_unavailable")
			return
		}
		used = append(append(used, g.used...), consumed)
		if len(used) > maxUsedRefresh {
			used = used[len(used)-maxUsedRefresh:]
		}
	default:
		oauthFail(w, r, 400, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}
	a, refresh := random(), random()
	expires := time.Now().Add(time.Hour)
	if g.Expires.Before(expires) {
		expires = g.Expires
	}
	// Persist before changing memory: a failed write keeps the previous
	// refresh token or the authorization code usable for a retry.
	if err := s.saveGrant(g, hash(a), expires, hash(refresh), used); err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	if f.Get("grant_type") == "refresh_token" {
		s.usedRefresh[consumed] = g
		g.used = used
		s.removeGrant(g) // Atomic rotation, including the previous access token.
	} else {
		if s.AuthMode == "client-secret" {
			s.consumed[consumed] = codeExpires
		}
		delete(s.codes, consumed)
	}
	s.access[hash(a)] = access{Grant: g, Expires: expires}
	s.refresh[hash(refresh)] = g
	jsonResponse(w, 200, map[string]any{"access_token": a, "refresh_token": refresh, "token_type": "Bearer", "expires_in": int(time.Until(expires) / time.Second), "scope": Scope})
}

func (s *Server) removeGrant(g *grant) {
	for k, a := range s.access {
		if a.Grant == g {
			delete(s.access, k)
		}
	}
	for k, r := range s.refresh {
		if r == g {
			delete(s.refresh, k)
		}
	}
}

func (s *Server) revokeGrant(g *grant) {
	s.removeGrant(g)
	s.deleteGrantFile(g)
	for k, v := range s.usedRefresh {
		if v == g {
			delete(s.usedRefresh, k)
		}
	}
}
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	f, ok := s.clientForm(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	k := hash(f.Get("token"))
	if g := s.refresh[k]; g != nil {
		s.revokeGrant(g)
	}
	if a, ok := s.access[k]; ok {
		s.revokeGrant(a.Grant)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
