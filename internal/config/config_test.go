package config

import (
	"strings"
	"testing"
)

func clean(t *testing.T) {
	t.Helper()
	for _, key := range []string{"MCP_ADDR", "MCP_PUBLIC_HOST", "MCP_STATE_DIR", "MCP_PUBLIC_URL", "MCP_REDIRECT_URI", "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "MCP_CLIENT_ID", "MCP_CLIENT_SECRET", "MCP_ALLOWED_USERS", "GITHUB_ALLOWED_USER_ID", "MCP_GOMAXPROCS", "MCP_ALLOW_DEFAULT_BRANCH_WRITES"} {
		t.Setenv(key, "")
	}
}
func TestAnonymousDefaults(t *testing.T) {
	clean(t)
	c, e := Load(nil)
	if e != nil {
		t.Fatal(e)
	}
	if c.OAuthEnabled() || c.GOMAXPROCS != 2 || c.Addr != "127.0.0.1:8181" {
		t.Fatalf("wrong defaults: %+v", c)
	}
}
func TestOAuthNeedsNoRepositoryAllowlist(t *testing.T) {
	clean(t)
	t.Setenv("GITHUB_CLIENT_ID", "id")
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("MCP_CLIENT_SECRET", strings.Repeat("s", 32))
	t.Setenv("MCP_PUBLIC_URL", "https://mcp.example.com")
	t.Setenv("GITHUB_ALLOWED_USER_ID", "42")
	c, e := Load(nil)
	if e != nil {
		t.Fatal(e)
	}
	if !c.OAuthEnabled() || c.GitHubScopes != "repo workflow" || c.AllowedUserID != 42 || len(c.AllowedUsers) != 0 {
		t.Fatalf("incorrect OAuth: %+v", c)
	}
}
func TestPartialCredentialsAndUnsafeCallbackRejected(t *testing.T) {
	clean(t)
	t.Setenv("GITHUB_CLIENT_ID", "id")
	if _, e := Load(nil); e == nil {
		t.Fatal("partial credentials accepted")
	}
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("MCP_CLIENT_SECRET", strings.Repeat("s", 32))
	t.Setenv("MCP_PUBLIC_URL", "https://mcp.example.com")
	t.Setenv("MCP_REDIRECT_URI", "https://attacker.example/callback")
	if _, e := Load(nil); e == nil {
		t.Fatal("unsafe callback accepted")
	}
}
