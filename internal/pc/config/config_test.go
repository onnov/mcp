package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestRootAndStateValidation(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "projects")
	os.Mkdir(root, 0700)
	state := filepath.Join(d, "state")
	c, e := Parse([]string{"--root", root, "--state", state, "--transport", "stdio"})
	if e != nil || c.Root != root {
		t.Fatal(c, e)
	}
	if _, e = Parse([]string{"--root", root, "--state", filepath.Join(root, "state"), "--transport", "stdio"}); e == nil {
		t.Fatal("state inside root accepted")
	}
	if _, e = Parse([]string{"--root", "/", "--state", state, "--transport", "stdio"}); e == nil {
		t.Fatal("filesystem root accepted")
	}
	if _, e = Parse([]string{"--root", root, "--state", state, "--transport", "stdio", "--max-seconds", "0"}); e == nil {
		t.Fatal("invalid timeout")
	}
}

func TestSOCKS5ProxyEnvironment(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "projects")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--root", root, "--state", filepath.Join(d, "state"), "--transport", "stdio"}
	t.Setenv("PC_MCP_SOCKS5_PROXY", "127.0.0.1:1084")
	c, err := Parse(args)
	if err != nil || c.SOCKS5Proxy == nil || c.SOCKS5Proxy.String() != "socks5h://127.0.0.1:1084" {
		t.Fatal("proxy environment not applied", err)
	}
	t.Setenv("PC_MCP_SOCKS5_PROXY", "http://127.0.0.1:1084")
	if _, err := Parse(args); err == nil {
		t.Fatal("invalid proxy did not fail at startup")
	}
	t.Setenv("PC_MCP_SOCKS5_PROXY", "")
	c, err = Parse(args)
	if err != nil || c.SOCKS5Proxy != nil {
		t.Fatal("empty proxy not disabled", err)
	}
}

func TestTransportEnvironmentAndSSHBoundaries(t *testing.T) {
	d := t.TempDir()
	root, state := filepath.Join(d, "projects"), filepath.Join(d, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("a-long-private-owner-password"), 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PC_MCP_TRANSPORT", "http")
	t.Setenv("PC_MCP_PUBLIC_URL", "https://pc.example.com")
	t.Setenv("PC_MCP_OAUTH_CLIENT_SECRET", "0123456789012345678901234567890123456789")
	t.Setenv("PC_MCP_OWNER_PASSWORD_HASH", string(hash))
	args := []string{"--root", root, "--state", state}
	c, err := Parse(args)
	if err != nil || c.Transport != "http" || c.HTTPAddr != "127.0.0.1:8182" {
		t.Fatal(c.Transport, err)
	}
	for _, addr := range []string{"0.0.0.0:8182", "192.168.1.10:8182", "localhost:8182", "127.0.0.1:0"} {
		t.Setenv("PC_MCP_HTTP_ADDR", addr)
		if _, err := Parse(args); err == nil {
			t.Fatal("public/ambiguous HTTP bind accepted", addr)
		}
	}
	t.Setenv("PC_MCP_HTTP_ADDR", "")
	t.Setenv("PC_MCP_OWNER_PASSWORD_HASH", "")
	if _, err := Parse(args); err == nil {
		t.Fatal("HTTP without owner authentication accepted")
	}
	t.Setenv("PC_MCP_OWNER_PASSWORD_HASH", string(hash))
	t.Setenv("PC_MCP_TRANSPORT", "ssh")
	t.Setenv("PC_MCP_SSH_ADDR", "server.example:22")
	t.Setenv("PC_MCP_SSH_USER", "owner")
	key, hosts := filepath.Join(d, "key"), filepath.Join(d, "known_hosts")
	os.WriteFile(key, []byte("test-key"), 0600)
	os.WriteFile(hosts, nil, 0600)
	t.Setenv("PC_MCP_SSH_KEY_FILE", key)
	t.Setenv("PC_MCP_SSH_KNOWN_HOSTS", hosts)
	c, err = Parse(args)
	if err != nil || c.Transport != "ssh" {
		t.Fatal(err)
	}
	t.Setenv("PC_MCP_SSH_REMOTE_ADDR", "0.0.0.0:18182")
	if _, err := Parse(args); err == nil {
		t.Fatal("public remote SSH listener accepted")
	}
	t.Setenv("PC_MCP_SSH_REMOTE_ADDR", "")
	inside := filepath.Join(root, "key")
	os.WriteFile(inside, []byte("test-key"), 0600)
	t.Setenv("PC_MCP_SSH_KEY_FILE", inside)
	if _, err := Parse(args); err == nil {
		t.Fatal("SSH key in workspace accepted")
	}
	jobCache := filepath.Join(d, "jobcache")
	os.Mkdir(jobCache, 0700)
	if err := os.Symlink(jobCache, filepath.Join(state, "cache")); err == nil {
		cachedKey := filepath.Join(jobCache, "key")
		os.WriteFile(cachedKey, []byte("test-key"), 0600)
		t.Setenv("PC_MCP_SSH_KEY_FILE", cachedKey)
		if _, err := Parse(args); err == nil {
			t.Fatal("SSH key in symlinked job cache accepted")
		}
	}
	if c, err := Parse(append(args, "--transport", "stdio")); err != nil || c.Transport != "stdio" {
		t.Fatal("stdio override no longer works", err)
	}
}
