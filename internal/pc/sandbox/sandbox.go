// Package sandbox runs every project command in a private Linux namespace.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Spec struct {
	Root        *os.Root
	CWD         string
	Args        []string
	HostNetwork bool
	Network     bool
	Seconds     int
	Credential  bool
}
type Runner interface {
	Run(context.Context, Spec, io.Writer, io.Writer) error
	Capabilities() map[string]any
}
type Engine struct {
	resourceMu sync.Mutex
	activeMu   sync.Mutex
	active     map[uint64]context.CancelCauseFunc
	sequence   uint64
	BwrapPath  string
	BwrapError string

	Cache               string
	Toolchains          []string
	AllowNetwork        bool
	MaxSeconds          int
	GHtoken             string
	State               string
	HelperPath          string
	HelperError         string
	RequireResources    bool
	BestEffortResources bool
	TmpMaxBytes         int64
	CgroupRoot          string
	ResourceError       string
	MemoryReserve       int64
	MemoryMax           int64
	DiskReserve         int64
	MaxProcesses        int
	AllowPrivateNetwork bool
	AllowHostNetwork    bool
}

func (e *Engine) Capabilities() map[string]any {
	m := map[string]any{"github_credentials_configured": e.GHtoken != "", "sandbox": "bubblewrap >= 0.12.0", "network_allowed": e.AllowNetwork, "max_seconds": e.MaxSeconds, "cpu_policy": "all available cores; no quota", "resource_isolation": e.RequireResources && e.ResourceError == "", "network_policy": "HTTP(S) proxy; public destinations only", "private_network_allowed": e.AllowPrivateNetwork, "host_network_allowed": e.AllowHostNetwork, "server_version": "1.1.5", "tool_schema_version": 5, "process_limit": e.MaxProcesses, "confirm_boundary": "commands have RW access to the checkout", "log_retention": "1 MiB / 10000 latest records per job"}
	for _, name := range []string{"git", "gh", "go", "python3", "node", "bwrap"} {
		p, err := exec.LookPath(name)
		if err == nil {
			m[name] = p
		}
	}
	if e.ResourceError != "" {
		m["resource_warning"] = e.ResourceError
	}
	m["process_limit_enforced"] = e.RequireResources && e.ResourceError == ""
	err := e.checkPlatform()
	m["network_policy"] = e.NetworkPolicy()
	if err == nil && e.RequireResources && e.ResourceError != "" {
		err = errors.New(e.ResourceError)
	}
	if err == nil && e.AllowNetwork && e.HelperError != "" {
		err = errors.New(e.HelperError)
	}
	m["execution_ready"] = err == nil
	if err != nil {
		m["execution_error"] = err.Error()
	}
	return m
}
func (e *Engine) NetworkPolicy() string {
	if e.AllowPrivateNetwork {
		return "private-proxy"
	}
	return "public-proxy"
}
func Validate(s Spec, maxSeconds int) error {
	if s.Root == nil {
		return errors.New("missing rooted workspace")
	}
	if !filepath.IsLocal(s.CWD) {
		return errors.New("cwd must be relative to the selected project")
	}
	if len(s.Args) == 0 || len(s.Args) > 128 || s.Args[0] == "" {
		return errors.New("command requires 1..128 arguments")
	}
	n := 0
	for _, a := range s.Args {
		n += len(a)
		if strings.IndexByte(a, 0) >= 0 {
			return errors.New("NUL in argument")
		}
	}
	if n > 65536 {
		return errors.New("arguments exceed 64 KiB")
	}
	if s.Seconds < 1 || s.Seconds > maxSeconds {
		return fmt.Errorf("seconds must be 1..%d", maxSeconds)
	}
	return nil
}
func (e *Engine) Run(ctx context.Context, s Spec, stdout, stderr io.Writer) error {
	if err := Validate(s, e.MaxSeconds); err != nil {
		return err
	}
	if s.HostNetwork && (!s.Network || s.Credential || !e.AllowHostNetwork) {
		return errors.New("direct host network requires operator allow-host-network, network:true and credential:false")
	}
	if s.Network && !e.AllowNetwork {
		return errors.New("network is disabled; restart with --allow-network if needed")
	}
	if s.Credential && !s.Network {
		return errors.New("GitHub authentication requires a network job")
	}
	if e.RequireResources && e.ResourceError != "" {
		return errors.New("resource isolation unavailable: " + e.ResourceError)
	}
	if s.Network && (e.HelperPath == "" || e.HelperError != "") {
		return errors.New("network helper unavailable: " + e.HelperError)
	}
	if err := e.checkPlatform(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.Seconds)*time.Second)
	defer cancel()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	e.activeMu.Lock()
	if e.active == nil {
		e.active = map[uint64]context.CancelCauseFunc{}
	}
	e.sequence++
	id := e.sequence
	e.active[id] = stop
	e.activeMu.Unlock()
	defer func() { e.activeMu.Lock(); delete(e.active, id); e.activeMu.Unlock() }()
	if s.Credential && e.GHtoken != "" {
		out, er := newRedactor(stdout, e.GHtoken), newRedactor(stderr, e.GHtoken)
		defer out.Close()
		defer er.Close()
		stdout, stderr = out, er
	}
	return e.run(ctx, s, stdout, stderr)
}

// Configure pins the sandbox executable before any project mutation. A binary
// in a model-writable project/cache cannot impersonate the isolation boundary.
func (e *Engine) Configure(root string) {
	if runtime.GOOS != "linux" {
		e.BwrapError = "execution requires Linux bubblewrap"
		return
	}
	p, err := exec.LookPath("bwrap")
	if err == nil {
		p, err = filepath.Abs(p)
	}
	if err == nil {
		p, err = filepath.EvalSymlinks(p)
	}
	if err != nil {
		e.BwrapError = "install bubblewrap >=0.12.0 before starting pc-mcp"
		return
	}
	for _, base := range []string{root, e.Cache} {
		rel, err := filepath.Rel(base, p)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			e.BwrapError = "bubblewrap executable must be outside model-writable workspace/cache"
			return
		}
	}
	e.BwrapPath = p
	e.configureResources()
	if e.AllowNetwork {
		e.configureHelper()
	}
}

// StopAll includes synchronous workspace Git commands as well as tracked jobs.
// It is independent of workspace/manager locks, so inspection cannot delay an
// emergency stop of another command.
func (e *Engine) StopAll() {
	e.activeMu.Lock()
	cancels := []context.CancelCauseFunc{}
	for _, cancel := range e.active {
		cancels = append(cancels, cancel)
	}
	e.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel(context.Canceled)
	}
}
