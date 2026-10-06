package config

import (
	"os"
	"path/filepath"
	"testing"
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
