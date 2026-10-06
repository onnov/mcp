// Package config validates operator-owned PC settings. No setting is model-controlled.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Root, State, Transport, TunnelID, APIKey, Organization string
	Toolchains                                             []string
	Network                                                bool
	MaxSeconds                                             int
}

func Parse(args []string) (Config, error) {
	c := Config{Root: os.Getenv("PC_MCP_ROOT"), State: os.Getenv("PC_MCP_STATE"), Transport: "tunnel", TunnelID: os.Getenv("CONTROL_PLANE_TUNNEL_ID"), APIKey: os.Getenv("CONTROL_PLANE_API_KEY"), Organization: os.Getenv("CONTROL_PLANE_ORGANIZATION_ID"), MaxSeconds: 900}
	fs := flag.NewFlagSet("pc-mcp", flag.ContinueOnError)
	fs.StringVar(&c.Root, "root", c.Root, "Required allowed workspace root (never / or your home)")
	fs.StringVar(&c.State, "state", c.State, "Private state/cache directory outside the workspace")
	fs.StringVar(&c.Transport, "transport", c.Transport, "tunnel or stdio (local clients/tests)")
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
	if c.Transport != "tunnel" && c.Transport != "stdio" {
		return c, errors.New("transport must be tunnel or stdio")
	}
	if c.Transport == "tunnel" && (c.TunnelID == "" || c.APIKey == "") {
		return c, errors.New("CONTROL_PLANE_TUNNEL_ID and CONTROL_PLANE_API_KEY are required for tunnel transport")
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
	return c, nil
}

func within(base, path string) bool {
	if base == "" || path == "" {
		return false
	}
	r, e := filepath.Rel(base, path)
	return e == nil && (r == "." || filepath.IsLocal(r))
}
