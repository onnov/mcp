package ownerauth

import (
	"html/template"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Доступ к PC MCP</title><style>body{font:16px system-ui;max-width:520px;margin:8vh auto;padding:24px;color:#202124}input,button{font:inherit;padding:12px;box-sizing:border-box;width:100%;margin-top:12px}button{cursor:pointer}small{color:#555}</style><h1>Подключить PC MCP</h1><p>Вы разрешаете ChatGPT читать и изменять проекты и запускать команды в разрешённом каталоге вашего ПК.</p><p>Продолжайте, только если вы начали подключение этого плагина в ChatGPT.</p><form method="post" action="/oauth/login"><input type="hidden" name="request" value="{{.}}"><label>Пароль владельца<input type="password" name="password" autocomplete="current-password" minlength="16" maxlength="72" required autofocus></label><button type="submit" name="action" value="allow">Разрешить доступ</button><button type="submit" name="action" value="deny" formnovalidate>Отмена</button></form><p><small>GitHub не используется. Разрешение действует до 7 дней; запуски, требующие подтверждения, проверяются отдельно.</small></p></html>`))

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		oauthError(w, 405, "invalid_request")
		return
	}
	q := r.URL.Query()
	if q.Get("client_id") != s.ClientID || q.Get("redirect_uri") != s.RedirectURI || q.Get("response_type") != "code" || q.Get("resource") != s.resource() || q.Get("state") == "" || len(q.Get("state")) > 1024 || (q.Get("scope") != "" && q.Get("scope") != Scope) || !validChallenge(q.Get("code_challenge")) || q.Get("code_challenge_method") != "S256" {
		oauthError(w, 400, "invalid_request")
		return
	}
	nonce, cookie := random(), random()
	s.mu.Lock()
	s.prune()
	if len(s.pending) >= 100 {
		s.mu.Unlock()
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	s.pending[hash(nonce)] = pending{State: q.Get("state"), Challenge: q.Get("code_challenge"), Cookie: hash(cookie), Expires: time.Now().Add(10 * time.Minute)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-pc-mcp-login", Value: cookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// HTML form POSTs inherit the document's referrer policy. no-referrer
	// would make their Origin opaque ("null"), failing our CSRF checks before
	// password verification. same-origin preserves it for /oauth/login while
	// withholding the authorization URL from the cross-origin callback.
	w.Header().Set("Referrer-Policy", "same-origin")
	// Browsers may apply form-action to the redirect after a form submission
	// too. Allow the validated, operator-configured ChatGPT callback as well
	// as the local form target, rather than allowing all external destinations.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+s.RedirectURI+"; frame-ancestors 'none'; base-uri 'none'")
	_ = loginPage.Execute(w, nonce)
}

func (s *Server) ownerLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		oauthError(w, 405, "invalid_request")
		return
	}
	// Do not trust forwarding headers. The public origin is operator configured.
	if r.Header.Get("Origin") != s.PublicURL {
		oauthError(w, 403, "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	cookie, err := r.Cookie("__Host-pc-mcp-login")
	if err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	key := hash(r.PostForm.Get("request"))
	s.mu.Lock()
	s.prune()
	p, ok := s.pending[key]
	if !ok || hash(cookie.Value) != p.Cookie {
		s.mu.Unlock()
		oauthError(w, 400, "invalid_request")
		return
	}
	delete(s.pending, key) // Even failed/cancelled attempts consume the browser flow.
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-pc-mcp-login", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	if r.PostForm.Get("action") == "deny" {
		s.redirect(w, r, p.State, "", "access_denied")
		return
	}
	password := r.PostForm.Get("password")
	if r.PostForm.Get("action") != "allow" || len(password) < 16 || len(password) > 72 {
		s.redirect(w, r, p.State, "", "access_denied")
		return
	}
	if !s.allowAttempt() {
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
		s.redirect(w, r, p.State, "", "access_denied")
		return
	}
	c := random()
	s.mu.Lock()
	s.prune()
	if len(s.codes) >= 100 {
		s.mu.Unlock()
		oauthError(w, 429, "temporarily_unavailable")
		return
	}
	s.codes[hash(c)] = code{Challenge: p.Challenge, Expires: time.Now().Add(time.Minute)}
	s.mu.Unlock()
	s.redirect(w, r, p.State, c, "")
}

func (s *Server) allowAttempt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	kept := s.attempts[:0]
	for _, t := range s.attempts {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	s.attempts = kept
	if len(s.attempts) >= 10 {
		return false
	}
	s.attempts = append(s.attempts, now)
	return true
}
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, state, code, failure string) {
	u, _ := url.Parse(s.RedirectURI)
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
