package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"github.com/onnov/mcp/internal/identity"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

func (a *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthError(w, 405, "invalid_request")
		return
	}
	q := r.URL.Query()
	if q.Get("client_id") != a.ClientID || q.Get("redirect_uri") != a.RedirectURI {
		oauthError(w, 400, "invalid_request")
		return
	}
	state := q.Get("state")
	if state == "" || len(state) > 1024 || strings.IndexFunc(state, unicode.IsControl) >= 0 {
		oauthError(w, 400, "invalid_request")
		return
	}
	if q.Get("response_type") != "code" {
		a.redirect(w, r, state, "", "unsupported_response_type")
		return
	}
	if q.Get("resource") != a.ResourceURL {
		a.redirect(w, r, state, "", "invalid_target")
		return
	}
	if scope := q.Get("scope"); scope != "" && scope != "github" {
		a.redirect(w, r, state, "", "invalid_scope")
		return
	}
	digest, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if err != nil || len(digest) != 32 || q.Get("code_challenge_method") != "S256" {
		a.redirect(w, r, state, "", "invalid_request")
		return
	}
	ghState, err := randomSecret()
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	verifier, err := randomSecret()
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	cookieValue, err := randomSecret()
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	a.mu.Lock()
	a.prune()
	if len(a.pending) >= 100 {
		a.mu.Unlock()
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	a.pending[tokenHash(ghState)] = oauthPending{State: state, Challenge: q.Get("code_challenge"), GitHubVerifier: verifier, CookieHash: tokenHash(cookieValue), Expires: time.Now().Add(10 * time.Minute)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-ghf-oauth", Value: cookieValue, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	u, _ := url.Parse("https://github.com/login/oauth/authorize")
	u.RawQuery = url.Values{
		"client_id": {a.GitHubClientID}, "redirect_uri": {a.PublicURL + "/oauth/github/callback"}, "scope": {a.GitHubScopes},
		"state": {ghState}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "allow_signup": {"false"},
	}.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (a *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthError(w, 405, "invalid_request")
		return
	}
	q := r.URL.Query()
	cookie, err := r.Cookie("__Host-ghf-oauth")
	if err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	key, cookieHash := tokenHash(q.Get("state")), tokenHash(cookie.Value)
	a.mu.Lock()
	a.prune()
	p, ok := a.pending[key]
	if !ok || subtle.ConstantTimeCompare(p.CookieHash[:], cookieHash[:]) != 1 {
		a.mu.Unlock()
		oauthError(w, 400, "invalid_request")
		return
	}
	delete(a.pending, key) // State is single-use, even when GitHub denies authorization.
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-ghf-oauth", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if q.Get("error") != "" || q.Get("code") == "" {
		a.redirect(w, r, p.State, "", "access_denied")
		return
	}
	values := url.Values{"client_id": {a.GitHubClientID}, "client_secret": {a.GitHubClientSecret}, "code": {q.Get("code")},
		"redirect_uri": {a.PublicURL + "/oauth/github/callback"}, "code_verifier": {p.GitHubVerifier}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		a.redirect(w, r, p.State, "", "server_error")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.GitHub.HTTP.Do(req)
	if err != nil {
		a.redirect(w, r, p.State, "", "temporarily_unavailable")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	var gt struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err != nil || len(data) > 64<<10 || resp.StatusCode != 200 || json.Unmarshal(data, &gt) != nil || gt.Error != "" || gt.AccessToken == "" || !strings.EqualFold(gt.TokenType, "bearer") {
		a.redirect(w, r, p.State, "", "access_denied")
		return
	}
	hasWriteScope := false
	for _, s := range strings.Fields(strings.ReplaceAll(gt.Scope, ",", " ")) {
		if s == "repo" {
			hasWriteScope = true
		}
	}
	if !hasWriteScope {
		a.redirect(w, r, p.State, "", "access_denied")
		return
	}
	grant := &oauthGrant{GitHubToken: gt.AccessToken, Expires: time.Now().Add(24 * time.Hour)}
	if gt.ExpiresIn > 0 && gt.ExpiresIn < int64((24*time.Hour)/time.Second) {
		grant.Expires = time.Now().Add(time.Duration(gt.ExpiresIn) * time.Second)
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	ctx := identity.With(r.Context(), grant)
	if err := a.GitHub.Get(ctx, "/user", "", &user); err != nil || user.ID <= 0 || user.Login == "" || (len(a.AllowedUsers) > 0 && !a.AllowedUsers[strings.ToLower(user.Login)]) || (a.AllowedUserID > 0 && a.AllowedUserID != user.ID) {
		a.redirect(w, r, p.State, "", "access_denied")
		return
	}
	grant.Login = user.Login
	grant.UserID = user.ID
	code, err := randomSecret()
	if err != nil {
		a.redirect(w, r, p.State, "", "server_error")
		return
	}
	a.mu.Lock()
	a.prune()
	if len(a.codes) >= 100 {
		a.mu.Unlock()
		a.redirect(w, r, p.State, "", "temporarily_unavailable")
		return
	}
	a.codes[tokenHash(code)] = oauthCode{Grant: grant, Challenge: p.Challenge, Expires: time.Now().Add(time.Minute)}
	a.mu.Unlock()
	a.redirect(w, r, p.State, code, "")
}
