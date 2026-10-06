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
	"time"
)

type Spec struct {
	Root       *os.Root
	CWD        string
	Args       []string
	Network    bool
	Seconds    int
	Credential bool
}
type Runner interface {
	Run(context.Context, Spec, io.Writer, io.Writer) error
	Capabilities() map[string]any
}
type Engine struct {
	BwrapPath  string
	BwrapError string

	Cache        string
	Toolchains   []string
	AllowNetwork bool
	MaxSeconds   int
	GHtoken      string
}

func (e *Engine) Capabilities() map[string]any {
	m := map[string]any{"github_credentials_configured": e.GHtoken != "", "sandbox": "bubblewrap >= 0.12.0", "network_allowed": e.AllowNetwork, "max_seconds": e.MaxSeconds}
	for _, name := range []string{"git", "gh", "go", "python3", "node", "bwrap"} {
		p, err := exec.LookPath(name)
		if err == nil {
			m[name] = p
		}
	}
	err := e.checkPlatform()
	m["execution_ready"] = err == nil
	if err != nil {
		m["execution_error"] = err.Error()
	}
	return m
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
	if s.Network && !e.AllowNetwork {
		return errors.New("network is disabled; restart with --allow-network if needed")
	}
	if s.Credential && !s.Network {
		return errors.New("GitHub authentication requires a network job")
	}
	if err := e.checkPlatform(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.Seconds)*time.Second)
	defer cancel()
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
}
