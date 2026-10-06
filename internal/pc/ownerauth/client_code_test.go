package ownerauth

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func clientServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, err := New(Config{PublicURL: "https://pc.example.com", ClientID: "owner-private-client", ClientSecret: strings.Repeat("s", 32), RedirectURI: "https://chatgpt.com/connector_platform_oauth_redirect", AuthMode: "client-secret"})
	if err != nil {
		t.Fatal(err)
	}
	m := http.NewServeMux()
	s.RegisterRoutes(m)
	m.Handle("/mcp", s.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	return s, m
}

func clientCode(t *testing.T, s *Server, h http.Handler) string {
	t.Helper()
	q := url.Values{"client_id": {s.ClientID}, "redirect_uri": {s.RedirectURI}, "response_type": {"code"}, "resource": {s.resource()}, "state": {"owner-state"}, "code_challenge": {challenge(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
	w := request(h, "GET", "/oauth/authorize?"+q.Encode(), nil, nil)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 303 || u.Query().Get("state") != "owner-state" || u.Query().Get("iss") != s.PublicURL || u.Query().Get("code") == "" {
		t.Fatal("passwordless authorization failed", w.Code, err)
	}
	return u.Query().Get("code")
}

func TestClientSecretModeStillRequiresAuthenticatedPKCE(t *testing.T) {
	s, h := clientServer(t)
	c := clientCode(t, s, h)
	if len(s.codes) != 0 || len(s.consumed) != 0 {
		t.Fatal("anonymous authorize allocated state")
	}
	if protected(h, "") != 401 {
		t.Fatal("anonymous MCP access")
	}
	for _, change := range []struct{ key, value string }{{"client_secret", ""}, {"client_secret", "wrong"}, {"code_verifier", strings.Repeat("w", 43)}, {"resource", "https://other.example/mcp"}, {"redirect_uri", "https://evil.example/callback"}} {
		f := tokenForm(s, c)
		f.Set(change.key, change.value)
		if w := request(h, "POST", "/oauth/token", f, nil); w.Code < 400 {
			t.Fatal("invalid exchange accepted", change.key)
		}
	}
	a, r := tokens(t, request(h, "POST", "/oauth/token", tokenForm(s, c), nil))
	if protected(h, a) != 204 {
		t.Fatal("owner access rejected")
	}
	if w := request(h, "POST", "/oauth/token", tokenForm(s, c), nil); w.Code != 400 {
		t.Fatal("code replay accepted")
	}
	f := url.Values{"grant_type": {"refresh_token"}, "client_id": {s.ClientID}, "client_secret": {s.ClientSecret}, "resource": {s.resource()}, "refresh_token": {r}}
	a2, _ := tokens(t, request(h, "POST", "/oauth/token", f, nil))
	if protected(h, a) != 401 || protected(h, a2) != 204 {
		t.Fatal("rotation failed")
	}
	request(h, "POST", "/oauth/token", f, nil)
	if protected(h, a2) != 401 {
		t.Fatal("refresh replay did not revoke family")
	}
}

func TestClientCodeSeparationExpiryAndConcurrentConsumption(t *testing.T) {
	s, h := clientServer(t)
	p, err := s.seal(pending{Challenge: challenge(strings.Repeat("v", 43)), Expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.openClientCode(p); err == nil {
		t.Fatal("login envelope accepted as code")
	}
	c, err := s.sealClientCode(code{Challenge: challenge(strings.Repeat("v", 43)), Expires: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "POST", "/oauth/token", tokenForm(s, c), nil); w.Code != 400 {
		t.Fatal("expired code accepted")
	}
	c = clientCode(t, s, h)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request(h, "POST", "/oauth/token", tokenForm(s, c), nil).Code }()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		}
	}
	if success != 1 {
		t.Fatal("code not single-use", success)
	}
	if w := request(h, "POST", "/oauth/login", nil, nil); w.Code != 404 {
		t.Fatal("password endpoint enabled in client-secret mode")
	}
}

func TestPasswordModeAndInvalidModeRemainExplicit(t *testing.T) {
	s, _ := clientServer(t)
	for _, mode := range []string{"", "password", "openai", "none"} {
		c := s.Config
		c.AuthMode = mode
		if _, err := New(c); err == nil {
			t.Fatal("unauthenticated or invalid configuration accepted", mode)
		}
	}
}
