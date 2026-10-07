package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerResourceAndAuthOptions(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "projects")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--root", root, "--state", filepath.Join(d, "state"), "--transport", "http"}
	t.Setenv("PC_MCP_PUBLIC_URL", "https://pc.example.com")
	t.Setenv("PC_MCP_OAUTH_CLIENT_SECRET", strings.Repeat("s", 32))
	t.Setenv("PC_MCP_OWNER_PASSWORD_HASH", "")
	t.Setenv("PC_MCP_OWNER_AUTH", "client-secret")
	t.Setenv("PC_MCP_RESOURCE_MODE", "auto")
	c, err := Parse(args)
	if err != nil || c.ResourceMode != "auto" || c.OAuth.AuthMode != "client-secret" {
		t.Fatal("owner settings not applied", err)
	}
	c, err = Parse(append(args, "--resource-mode", "strict"))
	if err != nil || c.ResourceMode != "strict" {
		t.Fatal("strict opt-in failed", err)
	}
	for _, flag := range []string{"--resource-mode", "--owner-auth"} {
		if _, err := Parse(append(args, flag, "invalid")); err == nil {
			t.Fatal("invalid mode accepted", flag)
		}
	}
	if _, err := Parse(append(args, "--owner-auth", "password")); err == nil {
		t.Fatal("password mode lost hash requirement")
	}
}
