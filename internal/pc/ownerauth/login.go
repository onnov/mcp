package ownerauth

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Доступ к PC MCP</title><style>body{font:16px system-ui;max-width:520px;margin:8vh auto;padding:24px;color:#202124}input,button{font:inherit;padding:12px;box-sizing:border-box;width:100%;margin-top:12px}button{cursor:pointer}small{color:#555}</style><h1>Подключить PC MCP</h1><p>Вы разрешаете {{.Client}} читать и изменять проекты и запускать команды в разрешённом каталоге вашего ПК.</p><p>Продолжайте, только если вы начали подключение этого коннектора в {{.Client}}.</p><form method="post" action="/oauth/login"><input type="hidden" name="request" value="{{.Request}}"><label>Пароль владельца<input type="password" name="password" autocomplete="current-password" minlength="16" maxlength="72" required autofocus></label><button type="submit" name="action" value="allow">Разрешить доступ</button><button type="submit" name="action" value="deny" formnovalidate>Отмена</button></form><p><small>GitHub не используется. Разрешение действует до 7 дней; запуски, требующие подтверждения, проверяются отдельно.</small></p></html>`))

// clientName labels the consent page by the validated callback host.
func clientName(redirectURI string) string {
	if u, err := url.Parse(redirectURI); err == nil && u.Host == "chatgpt.com" {
		return "ChatGPT"
	}
	return "Claude"
}

// authorizeProblem names the first invalid parameter. The client and callback
// are not trusted yet, so errors are shown here instead of being redirected.
func (s *Server) authorizeProblem(q url.Values) string {
	switch {
	case q.Get("client_id") != s.ClientID:
		return "unknown client_id; enter PC_MCP_OAUTH_CLIENT_ID as the OAuth client ID in the chat client"
	case !s.allowedRedirect(q.Get("redirect_uri")):
		return "redirect_uri is not an allowed ChatGPT or Claude callback: " + q.Get("redirect_uri")
	case q.Get("response_type") != "code":
		return "response_type must be code"
	case q.Get("resource") != s.resource():
		return "resource must be " + s.resource() + "; connect exactly this MCP URL"
	case q.Get("state") == "" || len(q.Get("state")) > 1024:
		return "state is required (up to 1024 characters)"
	case q.Get("scope") != "" && q.Get("scope") != Scope:
		return "scope must be " + Scope
	case !validChallenge(q.Get("code_challenge")) || q.Get("code_challenge_method") != "S256":
		return "PKCE S256 code_challenge is required"
	}
	return ""
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthFail(w, r, 405, "invalid_request", "authorization endpoint accepts only GET")
		return
	}
	q := r.URL.Query()
	if problem := s.authorizeProblem(q); problem != "" {
		oauthFail(w, r, 400, "invalid_request", problem)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !s.authorizeLimit.Allow(ClientIP(r, s.TrustedProxies)) {
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	if s.AuthMode == "client-secret" {
		c, err := s.sealClientCode(code{Challenge: q.Get("code_challenge"), RedirectURI: redirectURI, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			oauthError(w, 500, "server_error")
			return
		}
		s.redirect(w, r, redirectURI, q.Get("state"), c, "")
		return
	}
	cookie := random()
	nonce, err := s.seal(pending{State: q.Get("state"), Challenge: q.Get("code_challenge"), RedirectURI: redirectURI, Cookie: hash(cookie), Expires: time.Now().Add(10 * time.Minute)})
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}

	http.SetCookie(w, &http.Cookie{Name: "__Host-pc-mcp-login", Value: cookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// HTML form POSTs inherit the document's referrer policy. no-referrer
	// would make their Origin opaque ("null"), failing our CSRF checks before
	// password verification. same-origin preserves it for /oauth/login while
	// withholding the authorization URL from the cross-origin callback.
	w.Header().Set("Referrer-Policy", "same-origin")
	// Browsers may apply form-action to the redirect after a form submission
	// too. Allow only this request's validated callback as well as the local
	// form target, rather than allowing all external destinations.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+redirectURI+"; frame-ancestors 'none'; base-uri 'none'")
	_ = loginPage.Execute(w, struct{ Request, Client string }{nonce, clientName(redirectURI)})
}

func (s *Server) ownerLogin(w http.ResponseWriter, r *http.Request) {
	if s.AuthMode == "client-secret" {
		oauthFail(w, r, 404, "invalid_request", "owner password login is disabled in client-secret mode")
		return
	}
	if r.Method != http.MethodPost {
		oauthFail(w, r, 405, "invalid_request", "login form accepts only POST")
		return
	}
	// Do not trust forwarding headers. The public origin is operator configured.
	if r.Header.Get("Origin") != s.PublicURL {
		oauthFail(w, r, 403, "invalid_request", "Origin header "+strconv.Quote(r.Header.Get("Origin"))+" does not match PC_MCP_PUBLIC_URL "+s.PublicURL+"; open the login page via that URL")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		oauthFail(w, r, 400, "invalid_request", "cannot parse login form")
		return
	}
	cookie, err := r.Cookie("__Host-pc-mcp-login")
	if err != nil {
		oauthFail(w, r, 400, "invalid_request", "login cookie missing: the browser did not keep the cookie from the login page; start the connection again in one browser tab without blocking cookies")
		return
	}
	raw := r.PostForm.Get("request")
	key := hash(raw)
	p, err := s.openEnvelope(raw)
	if err != nil || hash(cookie.Value) != p.Cookie {
		oauthFail(w, r, 400, "invalid_request", "login form expired (10 minutes), was opened in another tab, or the server restarted; start the connection again from the chat")
		return
	}
	s.mu.Lock()
	s.prune()
	_, used := s.consumed[key]
	s.mu.Unlock()
	if used {
		oauthFail(w, r, 400, "invalid_request", "this login form was already used; start the connection again from the chat")
		return
	}

	http.SetCookie(w, &http.Cookie{Name: "__Host-pc-mcp-login", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if r.PostForm.Get("action") == "deny" {
		s.redirect(w, r, p.RedirectURI, p.State, "", "access_denied")
		return
	}
	password := r.PostForm.Get("password")
	if r.PostForm.Get("action") != "allow" || len(password) < 16 || len(password) > 72 {
		s.redirect(w, r, p.RedirectURI, p.State, "", "access_denied")
		return
	}
	if !s.loginLimit.Allow(ClientIP(r, s.TrustedProxies)) {
		w.Header().Set("Retry-After", "60")
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	select {
	case s.login <- struct{}{}:
		defer func() { <-s.login }()
	default:
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.PasswordHash), []byte(password)) != nil {
		s.redirect(w, r, p.RedirectURI, p.State, "", "access_denied")
		return
	}
	c := random()
	s.mu.Lock()
	s.prune()
	// Consume successful flows atomically; unauthenticated requests create no state.
	if _, used := s.consumed[key]; used {
		s.mu.Unlock()
		oauthFail(w, r, 400, "invalid_request", "this login form was already used; start the connection again from the chat")
		return
	}
	if len(s.codes) >= 100 || len(s.consumed) >= 1024 {
		s.mu.Unlock()
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	s.consumed[key] = p.Expires
	s.codes[hash(c)] = code{Challenge: p.Challenge, RedirectURI: p.RedirectURI, Expires: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	s.redirect(w, r, p.RedirectURI, p.State, c, "")
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, redirectURI, state, code, failure string) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("state", state)
	q.Set("iss", s.PublicURL)
	if failure != "" {
		q.Set("error", failure)
	} else {
		q.Set("code", code)
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}
