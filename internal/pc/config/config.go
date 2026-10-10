// Package config validates operator-owned PC settings. No setting is model-controlled.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
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
	CgroupRoot                                             string
	ResourceMode                                           string
	TmpMaxMiB                                              int64
	MemoryReserveMiB, MemoryMaxMiB, DiskReserveMiB         int64
	MaxProcesses                                           int
	MaxJobs                                                int
	PrivateNetwork, ConfirmCommands                        bool
	HostNetwork                                            bool
	// DebugRequests logs masked MCP request metadata to State/debug.
	DebugRequests bool
}

func Parse(args []string) (Config, error) {
	c := Config{Root: os.Getenv("PC_MCP_ROOT"), State: os.Getenv("PC_MCP_STATE"), Transport: env("PC_MCP_TRANSPORT", "tunnel"), TunnelID: os.Getenv("CONTROL_PLANE_TUNNEL_ID"), APIKey: os.Getenv("CONTROL_PLANE_API_KEY"), Organization: os.Getenv("CONTROL_PLANE_ORGANIZATION_ID"), MaxSeconds: 86400}
	fs := flag.NewFlagSet("pc-mcp", flag.ContinueOnError)
	fs.StringVar(&c.Root, "root", c.Root, "Required allowed workspace root (never / or your home)")
	fs.StringVar(&c.State, "state", c.State, "Private state/cache directory outside the workspace")
	fs.StringVar(&c.Transport, "transport", c.Transport, "tunnel, ssh, http, or stdio (also PC_MCP_TRANSPORT)")
	fs.StringVar(&c.TunnelID, "tunnel-id", c.TunnelID, "OpenAI Secure MCP Tunnel ID")
	network, err := strconv.ParseBool(env("PC_MCP_ALLOW_NETWORK", "false"))
	if err != nil {
		return c, errors.New("PC_MCP_ALLOW_NETWORK must be true or false")
	}
	fs.BoolVar(&c.Network, "allow-network", network, "Permit per-job public network access through the egress proxy (with confirmation)")
	fs.BoolVar(&c.HostNetwork, "allow-host-network", false, "Operator opt-in for approved jobs with direct host network (localhost/LAN/VPN); default is isolated proxy")
	fs.BoolVar(&c.PrivateNetwork, "allow-private-network", false, "Operator opt-in: proxy may reach localhost/LAN development services")
	fs.BoolVar(&c.ConfirmCommands, "confirm-commands", false, "Require confirmation for every arbitrary command; default grants local RW development execution")
	fs.StringVar(&c.CgroupRoot, "cgroup-root", os.Getenv("PC_MCP_CGROUP_ROOT"), "Delegated cgroup v2 directory; auto-detect current scope by default")
	fs.StringVar(&c.ResourceMode, "resource-mode", env("PC_MCP_RESOURCE_MODE", "auto"), "auto: use cgroups when delegated; strict: require cgroups before executing")
	fs.Int64Var(&c.TmpMaxMiB, "tmp-max-mib", 0, "Optional tmpfs ceiling; 0=kernel default in auto mode, 1024 MiB in strict mode")
	var ownerAuth string
	fs.StringVar(&ownerAuth, "owner-auth", env("PC_MCP_OWNER_AUTH", "password"), "password (default) or client-secret for a private single-owner OAuth client")
	fs.Int64Var(&c.MemoryReserveMiB, "memory-reserve-mib", 0, "Host RAM reserve; 0=auto (10% available, at least 512 MiB)")
	fs.Int64Var(&c.MemoryMaxMiB, "memory-max-mib", 0, "Optional job RAM ceiling; 0=all available RAM minus reserve")
	fs.Int64Var(&c.DiskReserveMiB, "disk-reserve-mib", 1024, "Stop jobs below this free-disk reserve (MiB)")
	fs.IntVar(&c.MaxJobs, "max-jobs", 4, "Maximum concurrent development jobs, 1..16")
	fs.IntVar(&c.MaxProcesses, "max-processes", 4096, "Maximum job processes/threads (cgroup pids.max)")
	fs.IntVar(&c.MaxSeconds, "max-seconds", 86400, "Maximum command duration, 1..86400 seconds")
	var trustedProxies string
	fs.StringVar(&trustedProxies, "trusted-proxies", os.Getenv("PC_MCP_TRUSTED_PROXIES"), "Comma-separated trusted reverse-proxy CIDRs; requires overwritten single X-Forwarded-For")
	var paths string
	fs.StringVar(&paths, "toolchains", "", "Comma-separated absolute read-only SDK directories outside /usr, /bin, /lib")
	if e := fs.Parse(args); e != nil {
		return c, e
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if c.ResourceMode != "auto" && c.ResourceMode != "strict" {
		return c, errors.New("resource-mode must be auto or strict")
	}
	if ownerAuth != "password" && ownerAuth != "client-secret" {
		return c, errors.New("owner-auth must be password or client-secret")
	}
	if c.TmpMaxMiB < 0 || c.TmpMaxMiB > 1<<30 {
		return c, errors.New("tmp-max-mib must be 0..2^30")
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
		c.OAuth.AuthMode = ownerAuth
		if c.DebugRequests, err = strconv.ParseBool(env("PC_MCP_DEBUG_REQUESTS", "false")); err != nil {
			return c, errors.New("PC_MCP_DEBUG_REQUESTS must be true or false")
		}
		persist, err := strconv.ParseBool(env("PC_MCP_OAUTH_PERSIST", "true"))
		if err != nil {
			return c, errors.New("PC_MCP_OAUTH_PERSIST must be true or false")
		}
		if persist {
			c.OAuth.StateDir = filepath.Join(c.State, "oauth")
		}
		for _, raw := range strings.Split(trustedProxies, ",") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil {
				return c, errors.New("trusted-proxies must contain numeric CIDRs")
			}
			c.OAuth.TrustedProxies = append(c.OAuth.TrustedProxies, prefix.Masked())
		}
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
			if e != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || within(cache, p) {
				return c, errors.New("SSH key/known_hosts must be regular files outside the job cache")
			}
			if within(c.Root, p) {
				fmt.Fprintln(os.Stderr, "pc-mcp: SSH configuration is inside the workspace; authorized development commands can access it")
			}
			*file = p
		}
	}
	if c.MaxJobs < 1 || c.MaxJobs > 16 {
		return c, errors.New("max-jobs must be 1..16")
	}
	if c.MemoryReserveMiB < 0 || c.MemoryMaxMiB < 0 || c.MemoryReserveMiB > 1<<30 || c.MemoryMaxMiB > 1<<30 || c.DiskReserveMiB < 64 || c.DiskReserveMiB > 1<<30 || c.MaxProcesses < 64 || c.MaxProcesses > 65536 {
		return c, errors.New("invalid resource limits: RAM 0..2^30 MiB, disk reserve 64..2^30 MiB, processes 64..65536")
	}
	if (c.PrivateNetwork || c.HostNetwork) && !c.Network {
		return c, errors.New("allow-private-network requires allow-network")
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
	if err := CheckSecrets(c.Root, c.APIKey, c.OAuth.ClientSecret, c.OAuth.PasswordHash, c.SSH.KeyPassphrase, os.Getenv("PC_MCP_GH_TOKEN")); err != nil {
		return c, err
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
