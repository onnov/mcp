// Package config owns environment and command-line configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type Config struct {
	Addr, PublicHost, StateDir         string
	PublicURL, RedirectURI             string
	GitHubClientID, GitHubClientSecret string
	MCPClientID, MCPClientSecret       string
	AllowedUsers                       map[string]bool
	AllowedUserID                      int64
	GitHubScopes                       string
	AllowDefaultBranchWrites           bool
	GOMAXPROCS                         int
	// DebugRequests writes masked request diagnostics to <StateDir>/debug.
	DebugRequests bool
}

func (c Config) OAuthEnabled() bool { return c.GitHubClientID != "" }

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}

func Load(args []string) (Config, error) {
	c := Config{
		Addr: env("MCP_ADDR", "127.0.0.1:8181"), PublicHost: os.Getenv("MCP_PUBLIC_HOST"),
		StateDir:       env("MCP_STATE_DIR", "./data"),
		PublicURL:      strings.TrimRight(os.Getenv("MCP_PUBLIC_URL"), "/"),
		RedirectURI:    env("MCP_REDIRECT_URI", "https://chatgpt.com/connector_platform_oauth_redirect"),
		GitHubClientID: os.Getenv("GITHUB_CLIENT_ID"), GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		MCPClientID: env("MCP_CLIENT_ID", "ghf-chatgpt"), MCPClientSecret: os.Getenv("MCP_CLIENT_SECRET"),
		GitHubScopes: "repo workflow", AllowedUsers: map[string]bool{}, GOMAXPROCS: 2,
	}
	for _, v := range strings.Split(os.Getenv("MCP_ALLOWED_USERS"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			c.AllowedUsers[v] = true
		}
	}
	var err error
	if s := os.Getenv("GITHUB_ALLOWED_USER_ID"); s != "" {
		c.AllowedUserID, err = strconv.ParseInt(s, 10, 64)
		if err != nil || c.AllowedUserID <= 0 {
			return c, errors.New("GITHUB_ALLOWED_USER_ID must be a positive numeric GitHub user ID")
		}
	}
	if s := os.Getenv("MCP_GOMAXPROCS"); s != "" {
		c.GOMAXPROCS, err = strconv.Atoi(s)
		if err != nil || c.GOMAXPROCS < 1 {
			return c, errors.New("MCP_GOMAXPROCS must be positive")
		}
	}
	if s := os.Getenv("MCP_ALLOW_DEFAULT_BRANCH_WRITES"); s != "" {
		c.AllowDefaultBranchWrites, err = strconv.ParseBool(s)
		if err != nil {
			return c, errors.New("invalid MCP_ALLOW_DEFAULT_BRANCH_WRITES boolean")
		}
	}
	if s := os.Getenv("MCP_DEBUG_REQUESTS"); s != "" {
		c.DebugRequests, err = strconv.ParseBool(s)
		if err != nil {
			return c, errors.New("invalid MCP_DEBUG_REQUESTS boolean")
		}
	}
	fs := flag.NewFlagSet("github-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.Addr, "addr", c.Addr, "HTTP listen address")
	fs.StringVar(&c.PublicHost, "public-host", c.PublicHost, "additional allowed Host")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if len(fs.Args()) != 0 {
		return c, errors.New("unexpected command-line arguments")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return c, fmt.Errorf("invalid listen address: %w", err)
	}
	if strings.ContainsAny(c.PublicHost, "/?#@ \\") || strings.IndexFunc(c.PublicHost, unicode.IsControl) >= 0 {
		return c, errors.New("invalid public host")
	}
	if c.StateDir == "" {
		return c, errors.New("MCP_STATE_DIR is required")
	}
	if c.GitHubClientID == "" && c.GitHubClientSecret == "" {
		return c, nil
	}
	if c.GitHubClientID == "" || c.GitHubClientSecret == "" || len(c.MCPClientSecret) < 32 {
		return c, errors.New("OAuth requires GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET and MCP_CLIENT_SECRET (32+ characters)")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("MCP_PUBLIC_URL must be an HTTPS origin without a path")
	}
	u, err = url.Parse(c.RedirectURI)
	if err != nil || u.Scheme != "https" || u.Host != "chatgpt.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("MCP_REDIRECT_URI must be an exact HTTPS ChatGPT callback")
	}
	return c, nil
}
