package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"github.com/onnov/mcp/internal/identity"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// authorizeProblem names the first invalid parameter that must not be
// redirected: the client and callback are not trusted yet.
func (a *Server) authorizeProblem(q url.Values) string {
	state := q.Get("state")
	switch {
	case q.Get("client_id") != a.ClientID:
		return "unknown client_id; enter MCP_CLIENT_ID as the OAuth Client ID in the chat client"
	case !a.allowedRedirect(q.Get("redirect_uri")):
		return "redirect_uri is not an allowed ChatGPT or Claude callback: " + q.Get("redirect_uri")
	case state == "" || len(state) > 1024 || strings.IndexFunc(state, unicode.IsControl) >= 0:
		return "state is required (up to 1024 characters, no control characters)"
	}
	return ""
}

// redirectProblem checks the parameters whose errors go back to the trusted callback.
func (a *Server) redirectProblem(q url.Values) (code, description string) {
	switch {
	case q.Get("response_type") != "code":
		return "unsupported_response_type", "response_type must be code"
	case q.Get("resource") != a.ResourceURL:
		return "invalid_target", "resource must be " + a.ResourceURL + "; connect exactly this MCP URL"
	case q.Get("scope") != "" && q.Get("scope") != "github":
		return "invalid_scope", "scope must be github"
	}
	digest, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if err != nil || len(digest) != 32 || q.Get("code_challenge_method") != "S256" {
		return "invalid_request", "PKCE S256 code_challenge is required"
	}
	return "", ""
}

func (a *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthFail(w, r, 405, "invalid_request", "authorization endpoint accepts only GET")
		return
	}
	q := r.URL.Query()
	if problem := a.authorizeProblem(q); problem != "" {
		oauthFail(w, r, 400, "invalid_request", problem)
		return
	}
	state, redirectURI := q.Get("state"), q.Get("redirect_uri")
	if code, description := a.redirectProblem(q); code != "" {
		a.redirectFail(w, r, redirectURI, state, code, description)
		return
	}
	ghState, err := randomSecret()
	if err != nil {
		oauthFail(w, r, 500, "server_error", "cannot generate random state")
		return
	}
	verifier, err := randomSecret()
	if err != nil {
		oauthFail(w, r, 500, "server_error", "cannot generate random state")
		return
	}
	cookieValue, err := randomSecret()
	if err != nil {
		oauthFail(w, r, 500, "server_error", "cannot generate random state")
		return
	}
	a.mu.Lock()
	a.prune()
	if len(a.pending) >= 100 {
		a.mu.Unlock()
		oauthFail(w, r, 429, "temporarily_unavailable", "too many unfinished authorizations; retry in 10 minutes")
		return
	}
	a.pending[tokenHash(ghState)] = oauthPending{State: state, RedirectURI: redirectURI, Challenge: q.Get("code_challenge"), GitHubVerifier: verifier, CookieHash: tokenHash(cookieValue), Expires: time.Now().Add(10 * time.Minute)}
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
		oauthFail(w, r, 405, "invalid_request", "GitHub callback accepts only GET")
		return
	}
	q := r.URL.Query()
	cookie, err := r.Cookie("__Host-ghf-oauth")
	if err != nil {
		oauthFail(w, r, 400, "invalid_request", "authorization cookie is missing; start the connection again from the same browser")
		return
	}
	key, cookieHash := tokenHash(q.Get("state")), tokenHash(cookie.Value)
	a.mu.Lock()
	a.prune()
	p, ok := a.pending[key]
	if !ok || subtle.ConstantTimeCompare(p.CookieHash[:], cookieHash[:]) != 1 {
		a.mu.Unlock()
		oauthFail(w, r, 400, "invalid_request", "unknown or expired (10 minutes) GitHub state, or another browser; start the connection again")
		return
	}
	delete(a.pending, key) // State is single-use, even when GitHub denies authorization.
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-ghf-oauth", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if q.Get("error") != "" || q.Get("code") == "" {
		a.redirectFail(w, r, p.RedirectURI, p.State, "access_denied", "GitHub authorization was denied or returned no code")
		return
	}
	values := url.Values{"client_id": {a.GitHubClientID}, "client_secret": {a.GitHubClientSecret}, "code": {q.Get("code")},
		"redirect_uri": {a.PublicURL + "/oauth/github/callback"}, "code_verifier": {p.GitHubVerifier}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(values.Encode()))
	if err != nil {
		a.redirectFail(w, r, p.RedirectURI, p.State, "server_error", "cannot build GitHub token request")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.GitHub.HTTP.Do(req)
	if err != nil {
		a.redirectFail(w, r, p.RedirectURI, p.State, "temporarily_unavailable", "GitHub token endpoint is unreachable")
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
		a.redirectFail(w, r, p.RedirectURI, p.State, "access_denied", "GitHub did not exchange the code for a bearer token (check GITHUB_CLIENT_ID/GITHUB_CLIENT_SECRET and the GitHub callback URL)")
		return
	}
	hasWriteScope := false
	for _, s := range strings.Fields(strings.ReplaceAll(gt.Scope, ",", " ")) {
		if s == "repo" {
			hasWriteScope = true
		}
	}
	if !hasWriteScope {
		a.redirectFail(w, r, p.RedirectURI, p.State, "access_denied", "GitHub token lacks the repo scope")
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
	if err := a.GitHub.Get(ctx, "/user", "", &user); err != nil || user.ID <= 0 || user.Login == "" {
		a.redirectFail(w, r, p.RedirectURI, p.State, "access_denied", "cannot read the GitHub user with the new token")
		return
	}
	if (len(a.AllowedUsers) > 0 && !a.AllowedUsers[strings.ToLower(user.Login)]) || (a.AllowedUserID > 0 && a.AllowedUserID != user.ID) {
		a.redirectFail(w, r, p.RedirectURI, p.State, "access_denied", "GitHub user "+user.Login+" is not allowed by MCP_ALLOWED_USERS/GITHUB_ALLOWED_USER_ID")
		return
	}
	grant.Login = user.Login
	grant.UserID = user.ID
	grant.Client = clientKey(p.RedirectURI)
	code, err := randomSecret()
	if err != nil {
		a.redirectFail(w, r, p.RedirectURI, p.State, "server_error", "cannot generate authorization code")
		return
	}
	a.mu.Lock()
	a.prune()
	if len(a.codes) >= 100 {
		a.mu.Unlock()
		a.redirectFail(w, r, p.RedirectURI, p.State, "temporarily_unavailable", "too many unused authorization codes")
		return
	}
	a.codes[tokenHash(code)] = oauthCode{Grant: grant, Challenge: p.Challenge, RedirectURI: p.RedirectURI, Expires: time.Now().Add(time.Minute)}
	a.mu.Unlock()
	a.redirect(w, r, p.RedirectURI, p.State, code, "")
}

// redirectFail logs the reason and returns the error to the request's own callback.
func (a *Server) redirectFail(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	log.Printf("github-mcp: OAuth %s %s rejected (%s): %s", r.Method, r.URL.Path, code, description)
	a.redirectWith(w, r, redirectURI, state, "", code, description)
}
