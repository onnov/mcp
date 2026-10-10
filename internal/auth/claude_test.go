package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Regression: Claude's callback used to get {"error":"invalid_request"}.
// Only the HTTP surface is used, so this test also runs against old code.
func TestClaudeCallbackAcceptedOverHTTP(t *testing.T) {
	a := testServer()
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	for _, callback := range []string{"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback"} {
		q := url.Values{"client_id": {a.ClientID}, "redirect_uri": {callback}, "response_type": {"code"}, "state": {"claude-state"}, "resource": {a.ResourceURL}, "code_challenge": {challenge(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil))
		if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "https://github.com/login/oauth/authorize?") {
			t.Fatalf("%s: %d %s", callback, w.Code, w.Body.String())
		}
	}
}
