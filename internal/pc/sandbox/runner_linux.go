//go:build linux

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/onnov/mcp/internal/pc/egress"
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
	// Only this project's cache is writable; siblings and control-plane state are absent.
	key := sha256.Sum256([]byte(s.Root.Name()))
	cache := filepath.Join(e.Cache, hex.EncodeToString(key[:]))
	if err = os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	cacheRoot, err := os.OpenRoot(cache)
	if err != nil {
		return err
	}
	defer cacheRoot.Close()
	cacheFD, err := cacheRoot.Open(".")
	if err != nil {
		return err
	}
	defer cacheFD.Close()
	ctx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)
	go e.guardDisk(ctx, cancelCause, s.Root.Name(), cache)
	var group *resourceGroup
	if e.RequireResources {
		group, err = e.newResourceGroup()
		if err != nil {
			return err
		}
		defer group.close()
	}
	var proxy *egress.Proxy
	gitSSH := s.Credential && len(s.Args) > 0 && s.Args[0] == "git" && e.SSHKeyFile != "" && e.SSHKnownHosts != ""
	if s.Network && !s.HostNetwork {
		proxy, err = egress.New(ctx, e.State, e.AllowPrivateNetwork, func() string {
			if s.Credential {
				return e.GHtoken
			}
			return ""
		}(), gitSSH)
		if err != nil {
			return err
		}
		defer proxy.Close()
		if gitSSH {
			passwd := fmt.Sprintf("pc:x:%d:%d:PC MCP:/home/pc:/bin/sh\n", os.Getuid(), os.Getgid())
			group := fmt.Sprintf("pc:x:%d:\n", os.Getgid())
			sshConfig := "Host github.com\n" +
				"  HostName github.com\n" +
				"  User git\n" +
				"  IdentityFile /run/pc-mcp-ssh-key\n" +
				"  IdentitiesOnly yes\n" +
				"  UserKnownHostsFile /run/pc-mcp-ssh-known-hosts\n" +
				"  StrictHostKeyChecking yes\n" +
				"  BatchMode yes\n" +
				"  ProxyCommand /usr/bin/nc -X connect -x 127.0.0.1:3128 %h %p\n" +
				"Host *\n" +
				"  BatchMode yes\n" +
				"  IdentitiesOnly yes\n" +
				"  ProxyCommand false\n"
			for name, data := range map[string]string{"passwd": passwd, "group": group, "ssh_config": sshConfig} {
				if err = os.WriteFile(filepath.Join(proxy.Directory, name), []byte(data), 0600); err != nil {
					return err
				}
			}
		}
	}
	a := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}
	if s.HostNetwork {
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
	a = append(a, "--proc", "/proc", "--dev", "/dev")
	if e.TmpMaxBytes > 0 {
		a = append(a, "--size", strconv.FormatInt(e.TmpMaxBytes, 10))
	}
	a = append(a, "--tmpfs", "/tmp", "--dir", "/home", "--dir", "/home/pc", "--dir", "/etc", "--dir", "/run")
	for _, p := range []string{"/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/alternatives", "/etc/ld.so.cache", "/etc/localtime"} {
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
	a = append(a, "--dir", "/workspace", "--dir", "/cache", "--bind", "/proc/self/fd/4", "/cache", "--bind", "/proc/self/fd/3", "/workspace")
	path := "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	for _, p := range e.Toolchains {
		path = filepath.Join(p, "bin") + ":" + path
	}
	vars := map[string]string{"PATH": path, "HOME": "/home/pc", "TMPDIR": "/tmp", "LANG": "C.UTF-8", "GOCACHE": "/cache/go-build", "GOMODCACHE": "/cache/go-mod", "GOPATH": "/cache/go", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off", "XDG_CACHE_HOME": "/cache", "PIP_CACHE_DIR": "/cache/pip", "npm_config_cache": "/cache/npm", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GH_CONFIG_DIR": "/home/pc/.config/gh", "GH_PROMPT_DISABLED": "1"}
	if s.Network && !s.HostNetwork {
		a = append(a, "--ro-bind", proxy.Directory, "/run/pc-mcp", "--ro-bind", e.HelperPath, "/pc-mcp-helper")
		if gitSSH {
			a = append(a,
				"--ro-bind", e.SSHKeyFile, "/run/pc-mcp-ssh-key",
				"--ro-bind", e.SSHKnownHosts, "/run/pc-mcp-ssh-known-hosts",
				"--ro-bind", filepath.Join(proxy.Directory, "passwd"), "/etc/passwd",
				"--ro-bind", filepath.Join(proxy.Directory, "group"), "/etc/group",
				"--ro-bind", filepath.Join(proxy.Directory, "ssh_config"), "/run/pc-mcp-ssh-config",
			)
		}
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			vars[key] = "http://127.0.0.1:3128"
		}
		vars["NO_PROXY"] = ""
		vars["no_proxy"] = ""
	}
	if s.Credential {
		if s.Args[0] != "git" && s.Args[0] != "gh" {
			return errors.New("credential jobs must invoke git or gh directly")
		}
		vars["GIT_ALLOW_PROTOCOL"] = "ssh:https"
		if e.GHtoken != "" {
			// Placeholder satisfies CLI login checks; the real token is added by the
			// host proxy only for verified github.com/api.github.com HTTPS requests.
			vars["GH_TOKEN"] = "pc-mcp-proxy-placeholder"
			vars["GH_HOST"] = "github.com"
			for _, key := range []string{"SSL_CERT_FILE", "GIT_SSL_CAINFO", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "npm_config_cafile"} {
				vars[key] = "/run/pc-mcp/ca.pem"
			}
		}
		if gitSSH {
			vars["GIT_SSH_COMMAND"] = "ssh -F /run/pc-mcp-ssh-config"
		}
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
	wrapper := `exec 3<&-; exec 4<&-; exec "$@"`
	if s.Network && !s.HostNetwork {
		wrapper = `exec 3<&-; exec 4<&-; /pc-mcp-helper --job-proxy /run/pc-mcp/net.sock & n=0; while ! test -f /tmp/pc-mcp-proxy-ready; do n=$((n+1)); if test "$n" -ge 100; then echo 'network helper did not start' >&2; exit 125; fi; sleep 0.05; done; exec "$@"`
	}
	a = append(a, "/bin/sh", "-c", wrapper, "pc-mcp")
	// Bound open FDs and single-file writes; CPU remains unrestricted.
	if _, err := os.Stat("/usr/bin/prlimit"); err == nil {
		limits := syscall.Rlimit{}
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
			return err
		}
		nofile := min(uint64(4096), limits.Max)
		a = append(a, "/usr/bin/prlimit", "--nofile="+strconv.FormatUint(nofile, 10)+":"+strconv.FormatUint(nofile, 10))
		{
			free, _, err := diskAvailable(cache)
			if err != nil {
				return err
			}
			projectFree, _, err := diskAvailable(s.Root.Name())
			if err != nil {
				return err
			}
			size := min(free, projectFree) - e.diskReserve()
			if size <= 0 {
				return errors.New("insufficient disk space after reserve")
			}
			a = append(a, "--fsize="+strconv.FormatInt(size, 10)+":"+strconv.FormatInt(size, 10))
		}
		a = append(a, "--")
	}
	a = append(a, s.Args...)
	cmd := exec.CommandContext(ctx, e.BwrapPath, a...)
	cmd.ExtraFiles = []*os.File{dir, cacheFD}
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if group != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(group.fd.Fd())
	}
	cmd.Cancel = func() error {
		if group != nil {
			if err := group.kill(); err == nil {
				return nil
			}
		}
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
		return context.Cause(ctx)
	}
	if group != nil && err != nil {
		events, _ := os.ReadFile(filepath.Join(group.directory, "memory.events"))
		for _, line := range strings.Split(string(events), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "oom_kill" && fields[1] != "0" {
				return fmt.Errorf("job exceeded available memory budget (host reserve protected): %w", err)
			}
		}
	}
	return err
}
