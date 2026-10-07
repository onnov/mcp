package ownerauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "a-long-private-owner-password"

var testHash string
var testHashOnce sync.Once

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	testHashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte(testPassword), 10)
		if err != nil {
			panic(err)
		}
		testHash = string(h)
	})
	s, err := New(Config{PublicURL: "https://pc.example.com", ClientID: "pc-chatgpt", ClientSecret: strings.Repeat("s", 32), RedirectURI: "https://chatgpt.com/connector_platform_oauth_redirect", PasswordHash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	mux.Handle("/mcp", s.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	return s, mux
}
func request(h http.Handler, method, path string, f url.Values, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body string
	if f != nil {
		body = f.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if f != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func begin(t *testing.T, s *Server, h http.Handler) (string, *http.Cookie) {
	t.Helper()
	q := url.Values{"client_id": {s.ClientID}, "redirect_uri": {s.RedirectURI}, "response_type": {"code"}, "resource": {s.resource()}, "scope": {Scope}, "state": {"original-state"}, "code_challenge": {challenge(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
	w := request(h, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("login page must preserve the same-origin form POST Origin")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'self' "+s.RedirectURI+";") {
		t.Fatal("login CSP must allow the configured OAuth callback")
	}
	m := regexp.MustCompile(`name="request" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal("missing CSRF nonce")
	}
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe owner login cookie")
	}
	return m[1], c
}
func authorizeCode(t *testing.T, s *Server, h http.Handler) string {
	t.Helper()
	nonce, cookie := begin(t, s, h)
	w := request(h, "POST", "/oauth/login", url.Values{"request": {nonce}, "password": {testPassword}, "action": {"allow"}}, map[string]string{"Origin": s.PublicURL}, cookie)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 303 || u.Query().Get("code") == "" || u.Query().Get("state") != "original-state" || u.Query().Get("iss") != s.PublicURL {
		t.Fatal(w.Code, w.Body.String(), u, err)
	}
	return u.Query().Get("code")
}
func tokenForm(s *Server, c string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {s.ClientID}, "client_secret": {s.ClientSecret}, "resource": {s.resource()}, "redirect_uri": {s.RedirectURI}, "code": {c}, "code_verifier": {strings.Repeat("v", 43)}}
}
func tokens(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var v struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Access == "" || v.Refresh == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	return v.Access, v.Refresh
}
func protected(h http.Handler, token string) int {
	return request(h, "POST", "/mcp", nil, map[string]string{"Authorization": "Bearer " + token}).Code
}

func TestOwnerOAuthPKCERotationAndRevocation(t *testing.T) {
	s, h := testServer(t)
	if protected(h, "") != 401 {
		t.Fatal("anonymous request accepted")
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server"} {
		if w := request(h, "GET", path, nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"pc"`) {
			t.Fatal(path, w.Code)
		}
	}
	c := authorizeCode(t, s, h)
	for _, change := range []struct{ key, value string }{{"code_verifier", strings.Repeat("w", 43)}, {"resource", "https://other.example/mcp"}, {"redirect_uri", "https://evil.example/callback"}, {"client_secret", "wrong"}, {"scope", "other"}} {
		f := tokenForm(s, c)
		f.Set(change.key, change.value)
		if w := request(h, "POST", "/oauth/token", f, nil); w.Code < 400 {
			t.Fatal("invalid token request accepted", change.key)
		}
	}
	a, refresh := tokens(t, request(h, "POST", "/oauth/token", tokenForm(s, c), nil))
	if protected(h, a) != 204 {
		t.Fatal("valid access token rejected")
	}
	if w := request(h, "POST", "/oauth/token", tokenForm(s, c), nil); w.Code != 400 {
		t.Fatal("authorization code replay")
	}
	f := url.Values{"grant_type": {"refresh_token"}, "client_id": {s.ClientID}, "client_secret": {s.ClientSecret}, "resource": {s.resource()}, "refresh_token": {refresh}}
	a2, r2 := tokens(t, request(h, "POST", "/oauth/token", f, nil))
	if a2 == a || r2 == refresh || protected(h, a) != 401 || protected(h, a2) != 204 {
		t.Fatal("token rotation failed")
	}
	if w := request(h, "POST", "/oauth/token", f, nil); w.Code != 400 {
		t.Fatal("refresh token replay")
	}
	if protected(h, a2) != 401 {
		t.Fatal("replayed refresh token did not revoke the token family")
	}
	f = url.Values{"client_id": {s.ClientID}, "client_secret": {s.ClientSecret}, "token": {r2}}
	if w := request(h, "POST", "/oauth/revoke", f, nil); w.Code != 200 || protected(h, a2) != 401 {
		t.Fatal("grant revocation failed")
	}
}

func TestLoginCSRFPasswordAndExpiry(t *testing.T) {
	s, h := testServer(t)
	nonce, cookie := begin(t, s, h)
	f := url.Values{"request": {nonce}, "password": {testPassword}, "action": {"allow"}}
	for _, origin := range []string{"https://evil.example", "null", ""} {
		if w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": origin}, cookie); w.Code != 403 {
			t.Fatal("unsafe login origin accepted", origin)
		}
	}
	if w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": s.PublicURL}); w.Code != 400 {
		t.Fatal("login without cookie accepted")
	}
	f.Set("password", "incorrect-owner-password")
	w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": s.PublicURL}, cookie)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" {
		t.Fatal("incorrect password accepted")
	}
	f.Set("password", testPassword)
	if w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": s.PublicURL}, cookie); w.Code != 303 {
		t.Fatal("valid retry rejected", w.Code)
	}
	if w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": s.PublicURL}, cookie); w.Code != 400 {
		t.Fatal("successful browser nonce replay accepted")
	}

	nonce, cookie = begin(t, s, h)
	f.Set("request", nonce)
	p, err := s.openEnvelope(nonce)
	if err != nil {
		t.Fatal(err)
	}
	p.Expires = time.Now().Add(-time.Second)
	expired, err := s.seal(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("request", expired)
	if w := request(h, "POST", "/oauth/login", f, map[string]string{"Origin": s.PublicURL}, cookie); w.Code != 400 {
		t.Fatal("expired login accepted")
	}
	c := authorizeCode(t, s, h)
	a, _ := tokens(t, request(h, "POST", "/oauth/token", tokenForm(s, c), nil))
	s.mu.Lock()
	v := s.access[hash(a)]
	v.Expires = time.Now().Add(-time.Second)
	s.access[hash(a)] = v
	s.mu.Unlock()
	if protected(h, a) != 401 {
		t.Fatal("expired access token accepted")
	}
}

func TestOwnerLoginRateLimitAndUnsafeConfiguration(t *testing.T) {
	s, h := testServer(t)
	for i := 0; i < 10; i++ {
		if !s.loginLimit.Allow("192.0.2.1") {
			t.Fatal("limit before 10")
		}
	}
	nonce, cookie := begin(t, s, h)
	w := request(h, "POST", "/oauth/login", url.Values{"request": {nonce}, "password": {testPassword}, "action": {"allow"}}, map[string]string{"Origin": s.PublicURL}, cookie)
	if w.Code != 429 {
		t.Fatal("login throttling failed", w.Code)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.PublicURL = "http://pc.example.com" }, func(c *Config) { c.RedirectURI = "https://evil.example/callback" }, func(c *Config) { c.PasswordHash = "plaintext" }, func(c *Config) { c.ClientSecret = "short" }} {
		c := s.Config
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Fatal("unsafe OAuth configuration accepted")
		}
	}
	if _, err := HashPassword([]byte("short")); err == nil {
		t.Fatal("weak owner password accepted")
	}
}

func TestConcurrentCodeConsumption(t *testing.T) {
	s, h := testServer(t)
	c := authorizeCode(t, s, h)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request(h, "POST", "/oauth/token", tokenForm(s, c), nil).Code }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for code := range results {
		if code == 200 {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("code was not single-use", successes)
	}
}

func TestBasicClientAuthenticationAndDeniedConsent(t *testing.T) {
	s, h := testServer(t)
	nonce, cookie := begin(t, s, h)
	w := request(h, "POST", "/oauth/login", url.Values{"request": {nonce}, "action": {"deny"}}, map[string]string{"Origin": s.PublicURL}, cookie)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" {
		t.Fatal("denied consent issued a code")
	}
	c := authorizeCode(t, s, h)
	f := tokenForm(s, c)
	f.Del("client_secret")
	f.Del("client_id")
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(url.QueryEscape(s.ClientID), url.QueryEscape(s.ClientSecret))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	a, _ := tokens(t, w)
	if protected(h, a) != 204 {
		t.Fatal("client_secret_basic failed")
	}
}
