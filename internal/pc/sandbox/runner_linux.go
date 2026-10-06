//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func checkPlatform() error {
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return errors.New("install bubblewrap >= 0.12.0; execution fails closed")
	}
	return checkVersion(p)
}
func (e *Engine) checkPlatform() error {
	if e.BwrapError != "" {
		return errors.New(e.BwrapError)
	}
	if e.BwrapPath == "" {
		return errors.New("bubblewrap executable is not configured")
	}
	if err := checkVersion(e.BwrapPath); err != nil {
		return err
	}
	if _, err := os.Stat("/usr/bin/prlimit"); err != nil {
		return errors.New("install util-linux prlimit for CPU/file-descriptor limits")
	}
	return nil
}
func checkVersion(p string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, p, "--version").Output()
	if err != nil {
		return err
	}
	var a, c, d int
	if _, err = fmt.Sscanf(strings.TrimSpace(string(b)), "bubblewrap %d.%d.%d", &a, &c, &d); err != nil || a == 0 && c < 12 {
		return errors.New("bubblewrap >=0.12.0 required (CVE-2026-87766); update your system package")
	}
	return nil
}
func (e *Engine) run(ctx context.Context, s Spec, stdout, stderr io.Writer) error {
	// Pass a pinned directory FD instead of checking a path and then reopening it.
	dir, err := s.Root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = os.MkdirAll(e.Cache, 0700); err != nil {
		return err
	}
	a := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}
	if s.Network {
		a = append(a, "--share-net")
	}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(p); err == nil {
			a = append(a, "--ro-bind", p, p)
		}
	}
	for _, p := range e.Toolchains {
		a = append(a, "--ro-bind", p, p)
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/home", "--dir", "/home/pc", "--dir", "/etc")
	for _, p := range []string{"/etc/ssl", "/etc/alternatives", "/etc/ld.so.cache", "/etc/localtime"} {
		if _, err := os.Stat(p); err == nil {
			a = append(a, "--ro-bind", p, p)
		}
	}
	if s.Network {
		for _, p := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf"} {
			if _, err := os.Stat(p); err == nil {
				a = append(a, "--ro-bind", p, p)
			}
		}
	}
	// Complete sandbox directory creation before binding untrusted project content.
	a = append(a, "--dir", "/workspace", "--dir", "/cache", "--bind", e.Cache, "/cache", "--bind", "/proc/self/fd/3", "/workspace")
	path := "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	for _, p := range e.Toolchains {
		path = filepath.Join(p, "bin") + ":" + path
	}
	vars := map[string]string{"PATH": path, "HOME": "/home/pc", "TMPDIR": "/tmp", "LANG": "C.UTF-8", "GOCACHE": "/cache/go-build", "GOMODCACHE": "/cache/go-mod", "GOPATH": "/cache/go", "GOTOOLCHAIN": "local", "GOMAXPROCS": "2", "GOTELEMETRY": "off", "XDG_CACHE_HOME": "/cache", "PIP_CACHE_DIR": "/cache/pip", "npm_config_cache": "/cache/npm", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GH_CONFIG_DIR": "/home/pc/.config/gh", "GH_PROMPT_DISABLED": "1"}
	if s.Credential {
		if e.GHtoken == "" {
			return errors.New("PC_MCP_GH_TOKEN is not configured; host gh login/SSH credentials are intentionally not mounted")
		}
		vars["GH_TOKEN"] = e.GHtoken
		if len(s.Args) == 0 || (s.Args[0] != "gh" && s.Args[0] != "git") {
			return errors.New("credential jobs must invoke git or gh directly")
		}
		// gh restricts credential delivery by GitHub host; no plaintext token in
		// command argv, .gitconfig or a model-readable file. Disable project hooks.
		vars["GIT_CONFIG_COUNT"] = "4"
		vars["GIT_CONFIG_KEY_0"] = "credential.helper"
		vars["GIT_CONFIG_VALUE_0"] = ""
		vars["GIT_CONFIG_KEY_1"] = "credential.helper"
		vars["GIT_CONFIG_VALUE_1"] = "!gh auth git-credential"
		vars["GIT_CONFIG_KEY_2"] = "core.hooksPath"
		vars["GIT_CONFIG_VALUE_2"] = "/dev/null"
		vars["GIT_CONFIG_KEY_3"] = "core.fsmonitor"
		vars["GIT_CONFIG_VALUE_3"] = "false"
	}
	for k, v := range vars {
		a = append(a, "--setenv", k, v)
	}
	a = append(a, "--chdir", filepath.Join("/workspace", s.CWD), "--")
	// bwrap deliberately passes inherited FDs to the payload. Close the pinned
	// HOST directory FD before any untrusted command can fchdir through it.
	// This constant shell wrapper does not evaluate user arguments.
	a = append(a, "/bin/sh", "-c", `exec 3<&-; exec "$@"`, "pc-mcp")
	// CPU deadline plus file-descriptor limit, in addition to wall-clock cancellation.
	if _, err := os.Stat("/usr/bin/prlimit"); err == nil {
		a = append(a, "/usr/bin/prlimit", "--nofile=1024:1024", "--cpu="+strconv.Itoa(s.Seconds+1)+":"+strconv.Itoa(s.Seconds+1), "--")
	}
	a = append(a, s.Args...)
	cmd := exec.CommandContext(ctx, e.BwrapPath, a...)
	cmd.ExtraFiles = []*os.File{dir}
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
