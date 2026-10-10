package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/onnov/mcp/internal/config"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testServer() *Server {
	return New(config.Config{PublicURL: "https://mcp.example.com", RedirectURI: "https://chatgpt.com/connector_platform_oauth_redirect", GitHubClientID: "gh-client", GitHubClientSecret: "gh-secret", MCPClientID: "client", MCPClientSecret: strings.Repeat("s", 32), GitHubScopes: "repo workflow"}, github.New(false))
}
func tokenRequest(a *Server, f url.Values) *httptest.ResponseRecorder {
	f.Set("client_id", a.ClientID)
	f.Set("client_secret", a.ClientSecret)
	f.Set("resource", a.ResourceURL)
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.token(w, r)
	return w
}

func TestPKCECodeReplayRefreshRotationAndPrincipal(t *testing.T) {
	a := testServer()
	verifier := strings.Repeat("v", 43)
	grant := &oauthGrant{UserID: 17, Login: "user", GitHubToken: "upstream-secret", Expires: time.Now().Add(time.Hour)}
	a.codes[tokenHash("code")] = oauthCode{Grant: grant, Challenge: challenge(verifier), RedirectURI: a.RedirectURI, Expires: time.Now().Add(time.Minute)}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"code"}, "redirect_uri": {a.RedirectURI}, "code_verifier": {strings.Repeat("x", 43)}}
	if w := tokenRequest(a, form); w.Code != 400 {
		t.Fatalf("bad verifier accepted: %d", w.Code)
	}
	form.Set("code_verifier", verifier)
	w := tokenRequest(a, form)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var pair struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &pair); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(w.Body.String(), grant.GitHubToken) {
		t.Fatal("upstream token exposed")
	}
	if w := tokenRequest(a, form); w.Code != 400 {
		t.Fatal("code replay accepted")
	}
	protected := a.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := identity.From(r.Context())
		if p == nil || p.UserID != 17 || p.GitHubToken != grant.GitHubToken {
			t.Fatal("identity lost")
		}
		w.WriteHeader(204)
	}))
	request := func(access string) int {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+access)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		return w.Code
	}
	if request(pair.Access) != 204 {
		t.Fatal("access rejected")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {pair.Refresh}}
	if w := tokenRequest(a, refresh); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if request(pair.Access) != 401 {
		t.Fatal("old access not revoked on rotation")
	}
	if w := tokenRequest(a, refresh); w.Code != 400 {
		t.Fatal("refresh replay accepted")
	}
}

func TestAuthorizeScopesResourceAndBrowserBinding(t *testing.T) {
	a := testServer()
	q := url.Values{"client_id": {a.ClientID}, "redirect_uri": {a.RedirectURI}, "response_type": {"code"}, "state": {"chat-state"}, "resource": {a.ResourceURL}, "code_challenge": {challenge(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
	w := httptest.NewRecorder()
	a.authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	if w.Code != 302 {
		t.Fatal(w.Body.String())
	}
	u, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	if u.Query().Get("scope") != "repo workflow" {
		t.Fatal("private/workflow scope missing")
	}
	state := u.Query().Get("state")
	bad := httptest.NewRequest("GET", "/oauth/github/callback?state="+state+"&code=gh", nil)
	bad.AddCookie(&http.Cookie{Name: "__Host-ghf-oauth", Value: "wrong-browser"})
	w = httptest.NewRecorder()
	a.githubCallback(w, bad)
	if w.Code != 400 {
		t.Fatal("callback accepted another browser")
	}
	if len(a.pending) != 1 {
		t.Fatal("attacker consumed another browser's pending state")
	}
	q.Set("resource", "https://attacker.example/mcp")
	w = httptest.NewRecorder()
	a.authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	redirect, _ := url.Parse(w.Header().Get("Location"))
	if redirect.Query().Get("error") != "invalid_target" {
		t.Fatal("wrong resource accepted")
	}
}

func TestCallbackUsesImmutableUserIDAndEnforcesOptionalRestriction(t *testing.T) {
	for _, allowed := range []int64{0, 42, 99} {
		t.Run(string(rune('a'+allowed)), func(t *testing.T) {
			a := testServer()
			a.AllowedUserID = allowed
			a.pending[tokenHash("state")] = oauthPending{State: "chat-state", RedirectURI: a.RedirectURI, CookieHash: tokenHash("browser"), Challenge: "challenge", GitHubVerifier: "verifier", Expires: time.Now().Add(time.Minute)}
			a.GitHub.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"access_token":"gh-token","token_type":"bearer","scope":"repo,workflow"}`
				if r.URL.Host == "api.github.com" {
					if r.Header.Get("Authorization") != "Bearer gh-token" {
						t.Fatal("upstream token not bound")
					}
					body = `{"id":42,"login":"renamable-login"}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			r := httptest.NewRequest("GET", "/oauth/github/callback?state=state&code=gh-code", nil)
			r.AddCookie(&http.Cookie{Name: "__Host-ghf-oauth", Value: "browser"})
			w := httptest.NewRecorder()
			a.githubCallback(w, r)
			u, _ := url.Parse(w.Header().Get("Location"))
			if allowed == 99 {
				if u.Query().Get("error") != "access_denied" || len(a.codes) != 0 {
					t.Fatal("wrong account allowed")
				}
				return
			}
			if u.Query().Get("code") == "" {
				t.Fatal(w.Header())
			}
			for _, v := range a.codes {
				if v.Grant.UserID != 42 || v.Grant.Login != "renamable-login" {
					t.Fatal("identity not recorded")
				}
			}
		})
	}
}

// fakeGitHub answers the GitHub token exchange and /user lookups.
func fakeGitHub(a *Server) {
	a.GitHub.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"access_token":"gh-token","token_type":"bearer","scope":"repo,workflow"}`
		if r.URL.Host == "api.github.com" {
			body = `{"id":42,"login":"owner"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func oauthBody(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	return body
}

// authorizeThrough runs /oauth/authorize and the GitHub callback for one
// callback URI and returns the chat client's redirect.
func authorizeThrough(t *testing.T, a *Server, redirectURI, verifier string) *url.URL {
	t.Helper()
	q := url.Values{"client_id": {a.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "state": {"chat-state"}, "resource": {a.ResourceURL}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	w := httptest.NewRecorder()
	a.authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
	if w.Code != 302 {
		t.Fatalf("authorize %s: %d %s", redirectURI, w.Code, w.Body.String())
	}
	toGitHub, _ := url.Parse(w.Header().Get("Location"))
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/oauth/github/callback?state="+url.QueryEscape(toGitHub.Query().Get("state"))+"&code=gh-code", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	a.githubCallback(w, r)
	back, err := url.Parse(w.Header().Get("Location"))
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("callback: %d %v", w.Code, w.Header())
	}
	return back
}

func TestClaudeFlowBindsCodeToItsCallback(t *testing.T) {
	a := testServer()
	fakeGitHub(a)
	verifier := strings.Repeat("v", 43)
	for _, claude := range ClaudeRedirectURIs {
		back := authorizeThrough(t, a, claude, verifier)
		if back.Scheme+"://"+back.Host+back.Path != claude || back.Query().Get("state") != "chat-state" {
			t.Fatalf("redirected to %s, want %s", back, claude)
		}
		form := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {a.RedirectURI}, "code_verifier": {verifier}}
		w := tokenRequest(a, form)
		if w.Code != 400 || oauthBody(t, w)["error"] != "invalid_grant" || !strings.Contains(oauthBody(t, w)["error_description"], "redirect_uri") {
			t.Fatalf("code exchanged with another callback: %d %s", w.Code, w.Body.String())
		}
		form.Set("redirect_uri", claude)
		if w := tokenRequest(a, form); w.Code != 200 {
			t.Fatalf("Claude exchange: %d %s", w.Code, w.Body.String())
		}
	}
	// ChatGPT keeps working with the same client.
	back := authorizeThrough(t, a, a.RedirectURI, verifier)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {ClaudeRedirectURIs[0]}, "code_verifier": {verifier}}
	if w := tokenRequest(a, form); w.Code != 400 {
		t.Fatal("ChatGPT code exchanged with a Claude callback")
	}
	form.Set("redirect_uri", a.RedirectURI)
	if w := tokenRequest(a, form); w.Code != 200 {
		t.Fatalf("ChatGPT exchange: %s", w.Body.String())
	}
	clients := map[string]int{}
	for _, v := range a.access {
		clients[v.Grant.Client]++
	}
	if clients["claude"] != 2 || clients["chatgpt"] != 1 {
		t.Fatalf("grants not labelled by client: %v", clients)
	}
}

func TestOAuthRejectionsExplainTheStep(t *testing.T) {
	a := testServer()
	base := url.Values{"client_id": {a.ClientID}, "redirect_uri": {ClaudeRedirectURIs[0]}, "response_type": {"code"}, "state": {"s"}, "resource": {a.ResourceURL}, "code_challenge": {challenge(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
	with := func(key, value string) url.Values {
		q := url.Values{}
		for k, v := range base {
			q[k] = v
		}
		q.Set(key, value)
		return q
	}
	for _, tc := range []struct{ key, value, want string }{
		{"client_id", "other", "client_id"},
		{"redirect_uri", "https://attacker.example/cb", "redirect_uri"},
		{"state", "", "state"},
	} {
		w := httptest.NewRecorder()
		a.authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+with(tc.key, tc.value).Encode(), nil))
		if w.Code != 400 || !strings.Contains(oauthBody(t, w)["error_description"], tc.want) {
			t.Fatalf("%s: %d %s", tc.key, w.Code, w.Body.String())
		}
	}
	// Errors after the callback is trusted go back to that callback.
	w := httptest.NewRecorder()
	a.authorize(w, httptest.NewRequest("GET", "/oauth/authorize?"+with("resource", "https://attacker.example/mcp").Encode(), nil))
	back, _ := url.Parse(w.Header().Get("Location"))
	if !strings.HasPrefix(back.String(), ClaudeRedirectURIs[0]) || back.Query().Get("error") != "invalid_target" || back.Query().Get("error_description") == "" {
		t.Fatalf("resource error: %s", back)
	}
	f := url.Values{"grant_type": {"authorization_code"}, "client_id": {a.ClientID}, "client_secret": {"wrong"}, "resource": {a.ResourceURL}}
	r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	a.token(w, r)
	if w.Code != 401 || !strings.Contains(oauthBody(t, w)["error_description"], "MCP_CLIENT_SECRET") {
		t.Fatalf("client error: %d %s", w.Code, w.Body.String())
	}
	if w := tokenRequest(a, url.Values{"grant_type": {"password"}}); oauthBody(t, w)["error_description"] == "" {
		t.Fatal("grant_type error without description")
	}
}
