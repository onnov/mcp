// Package config validates operator-owned PC settings. No setting is model-controlled.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onnov/mcp/internal/pc/netproxy"
	"github.com/onnov/mcp/internal/pc/ownerauth"
	"github.com/onnov/mcp/internal/pc/sshtunnel"
)

type Config struct {
	Root, State, Transport, TunnelID, APIKey, Organization string
	Toolchains                                             []string
	Network                                                bool
	MaxSeconds                                             int
	SOCKS5Proxy                                            *url.URL
	HTTPAddr                                               string
	OAuth                                                  ownerauth.Config
	SSH                                                    sshtunnel.Config
}

func Parse(args []string) (Config, error) {
	c := Config{Root: os.Getenv("PC_MCP_ROOT"), State: os.Getenv("PC_MCP_STATE"), Transport: env("PC_MCP_TRANSPORT", "tunnel"), TunnelID: os.Getenv("CONTROL_PLANE_TUNNEL_ID"), APIKey: os.Getenv("CONTROL_PLANE_API_KEY"), Organization: os.Getenv("CONTROL_PLANE_ORGANIZATION_ID"), MaxSeconds: 900}
	fs := flag.NewFlagSet("pc-mcp", flag.ContinueOnError)
	fs.StringVar(&c.Root, "root", c.Root, "Required allowed workspace root (never / or your home)")
	fs.StringVar(&c.State, "state", c.State, "Private state/cache directory outside the workspace")
	fs.StringVar(&c.Transport, "transport", c.Transport, "tunnel, ssh, http, or stdio (also PC_MCP_TRANSPORT)")
	fs.StringVar(&c.TunnelID, "tunnel-id", c.TunnelID, "OpenAI Secure MCP Tunnel ID")
	fs.BoolVar(&c.Network, "allow-network", false, "Permit individual jobs to request network access")
	fs.IntVar(&c.MaxSeconds, "max-seconds", 900, "Maximum command duration, 1..86400 seconds")
	var paths string
	fs.StringVar(&paths, "toolchains", "", "Comma-separated absolute read-only SDK directories outside /usr, /bin, /lib")
	if e := fs.Parse(args); e != nil {
		return c, e
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	proxy, proxyErr := netproxy.Parse(os.Getenv("PC_MCP_SOCKS5_PROXY"))
	if proxyErr != nil {
		return c, proxyErr
	}
	c.SOCKS5Proxy = proxy
	if c.Root == "" {
		return c, errors.New("--root or PC_MCP_ROOT is required")
	}
	var e error
	c.Root, e = filepath.Abs(c.Root)
	if e != nil {
		return c, e
	}
	c.Root, e = filepath.EvalSymlinks(c.Root)
	if e != nil {
		return c, e
	}
	home, _ := os.UserHomeDir()
	if within(c.Root, home) || c.Root == string(filepath.Separator) || c.Root == home {
		return c, errors.New("choose a dedicated projects directory, not / or your home")
	}
	st, e := os.Stat(c.Root)
	if e != nil || !st.IsDir() {
		return c, errors.New("root must be an existing directory")
	}
	if c.State == "" {
		base, e := os.UserConfigDir()
		if e != nil {
			return c, e
		}
		c.State = filepath.Join(base, "pc-mcp")
	}
	c.State, e = filepath.Abs(c.State)
	if e != nil {
		return c, e
	}
	if e = os.MkdirAll(c.State, 0700); e != nil {
		return c, e
	}
	c.State, e = filepath.EvalSymlinks(c.State)
	if e != nil {
		return c, e
	}
	rel, e := filepath.Rel(c.Root, c.State)
	if e != nil {
		return c, e
	}
	if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		return c, errors.New("state must be outside the allowed workspace")
	}
	if c.State == home || within(c.State, c.Root) {
		return c, errors.New("state must be a dedicated directory, separate from the workspace and home root")
	}
	if e = os.Chmod(c.State, 0700); e != nil {
		return c, e
	}
	if c.Transport != "tunnel" && c.Transport != "stdio" && c.Transport != "http" && c.Transport != "ssh" {
		return c, errors.New("transport must be tunnel, ssh, http, or stdio")
	}
	if c.Transport == "tunnel" && (c.TunnelID == "" || c.APIKey == "") {
		return c, errors.New("CONTROL_PLANE_TUNNEL_ID and CONTROL_PLANE_API_KEY are required for tunnel transport")
	}
	if c.Transport == "http" || c.Transport == "ssh" {
		c.HTTPAddr = env("PC_MCP_HTTP_ADDR", "127.0.0.1:8182")
		if !loopbackPort(c.HTTPAddr) {
			return c, errors.New("PC_MCP_HTTP_ADDR must be a numeric loopback IP:port (1..65535)")
		}
		c.OAuth = ownerauth.Config{PublicURL: strings.TrimRight(os.Getenv("PC_MCP_PUBLIC_URL"), "/"), ClientID: env("PC_MCP_OAUTH_CLIENT_ID", "pc-mcp-chatgpt"), ClientSecret: os.Getenv("PC_MCP_OAUTH_CLIENT_SECRET"), RedirectURI: env("PC_MCP_OAUTH_REDIRECT_URI", "https://chatgpt.com/connector_platform_oauth_redirect"), PasswordHash: os.Getenv("PC_MCP_OWNER_PASSWORD_HASH")}
		if e := c.OAuth.Validate(); e != nil {
			return c, e
		}
	}
	if c.Transport == "ssh" {
		c.SSH = sshtunnel.Config{Addr: os.Getenv("PC_MCP_SSH_ADDR"), User: os.Getenv("PC_MCP_SSH_USER"), KeyFile: os.Getenv("PC_MCP_SSH_KEY_FILE"), KeyPassphrase: os.Getenv("PC_MCP_SSH_KEY_PASSPHRASE"), KnownHosts: os.Getenv("PC_MCP_SSH_KNOWN_HOSTS"), RemoteAddr: env("PC_MCP_SSH_REMOTE_ADDR", "127.0.0.1:18182")}
		host, port, e := net.SplitHostPort(c.SSH.Addr)
		n, portErr := strconv.Atoi(port)
		if e != nil || host == "" || portErr != nil || n < 1 || n > 65535 || c.SSH.User == "" {
			return c, errors.New("SSH mode requires PC_MCP_SSH_ADDR=host:port and PC_MCP_SSH_USER")
		}
		if !loopbackPort(c.SSH.RemoteAddr) {
			return c, errors.New("PC_MCP_SSH_REMOTE_ADDR must be a numeric loopback IP:port (1..65535)")
		}
		cache := filepath.Join(c.State, "cache")
		if resolved, err := filepath.EvalSymlinks(cache); err == nil {
			cache = resolved
		}
		for _, file := range []*string{&c.SSH.KeyFile, &c.SSH.KnownHosts} {
			if *file == "" {
				return c, errors.New("SSH mode requires PC_MCP_SSH_KEY_FILE and PC_MCP_SSH_KNOWN_HOSTS")
			}
			p, e := filepath.Abs(*file)
			if e == nil {
				p, e = filepath.EvalSymlinks(p)
			}
			if e != nil {
				return c, errors.New("SSH key/known_hosts must be existing files")
			}
			info, e := os.Stat(p)
			if e != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || within(c.Root, p) || within(cache, p) {
				return c, errors.New("SSH key/known_hosts must be regular files outside the workspace and job cache")
			}
			*file = p
		}
	}
	if c.MaxSeconds < 1 || c.MaxSeconds > 86400 {
		return c, errors.New("max-seconds must be 1..86400")
	}
	for _, p := range strings.Split(paths, ",") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		p, e = filepath.Abs(strings.TrimSpace(p))
		if e != nil {
			return c, e
		}
		p, e = filepath.EvalSymlinks(p)
		if e != nil {
			return c, e
		}
		s, e := os.Stat(p)
		if e != nil || !s.IsDir() {
			return c, fmt.Errorf("invalid toolchain directory %s", p)
		}
		if p == "/" || p == home || within(c.Root, p) || within(p, c.Root) || within(c.State, p) || within(p, c.State) || within(p, home) {
			return c, errors.New("toolchain must be a dedicated SDK directory")
		}
		c.Toolchains = append(c.Toolchains, p)
	}
	if c.Transport == "ssh" {
		for _, sdk := range c.Toolchains {
			if within(sdk, c.SSH.KeyFile) || within(sdk, c.SSH.KnownHosts) {
				return c, errors.New("SSH credentials cannot be inside sandbox toolchain mounts")
			}
		}
	}
	return c, nil
}

func within(base, path string) bool {
	if base == "" || path == "" {
		return false
	}
	r, e := filepath.Rel(base, path)
	return e == nil && (r == "." || filepath.IsLocal(r))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func loopbackPort(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	n, e := strconv.Atoi(port)
	ip := net.ParseIP(host)
	return err == nil && e == nil && n > 0 && n <= 65535 && ip != nil && ip.IsLoopback()
}
