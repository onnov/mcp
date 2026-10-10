// Package workspace coordinates selection, branch transitions and filesystem
// mutations. Commands share a checkout execution lease; branch transitions
// remain locked until every child exits. Live file edits support watch servers.
package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/onnov/mcp/internal/pc/files"
	"github.com/onnov/mcp/internal/pc/sandbox"
)

type Target struct {
	Directory string `json:"directory" jsonschema:"Explicit directory relative to configured root; use dot for root"`
	Branch    string `json:"branch" jsonschema:"Exact current branch; empty only for directories without Git"`
}
type Info struct {
	Target
	Remembered         bool     `json:"remembered"`
	Available          bool     `json:"available"`
	GitStatusAvailable bool     `json:"git_status_available"`
	Git                bool     `json:"git"`
	Branches           []string `json:"branches"`
	Dirty              bool     `json:"dirty"`
	GitRoot            string   `json:"git_root,omitempty"`
	Message            string   `json:"message,omitempty"`
	SessionBound       bool     `json:"session_bound"`
}
type persisted struct {
	Root      string `json:"root"`
	Selection Target `json:"selection"`
}
type executionLease struct {
	count     int
	exclusive bool
}
type Service struct {
	mu              sync.Mutex
	root            *os.Root
	rootPath, state string
	sessionsState   string
	runner          sandbox.Runner
	selected        Target
	sessions        map[string]sessionBinding
	busy            map[string]executionLease
	remembered      bool
}

func New(rootPath, state string, runner sandbox.Runner) (*Service, error) {
	r, e := os.OpenRoot(rootPath)
	if e != nil {
		return nil, e
	}
	s := &Service{root: r, rootPath: rootPath, state: filepath.Join(state, "selection.json"), sessionsState: filepath.Join(state, "chat-workspaces.json"), runner: runner, selected: Target{Directory: "."}, sessions: map[string]sessionBinding{}, busy: map[string]executionLease{}}
	b, e := os.ReadFile(s.state)
	if e == nil {
		var p persisted
		if e = json.Unmarshal(b, &p); e != nil {
			r.Close()
			return nil, fmt.Errorf("invalid selection state: %w", e)
		}
		if p.Root == rootPath && files.Valid(p.Selection.Directory) == nil {
			s.selected = p.Selection
			s.remembered = true
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		r.Close()
		return nil, e
	}
	if e = s.loadSessions(); e != nil {
		r.Close()
		return nil, e
	}
	return s, nil
}
func (s *Service) Close() error                 { return s.root.Close() }
func (s *Service) Capabilities() map[string]any { return s.runner.Capabilities() }
func (s *Service) List(path string, offset, limit int, query string) (files.Page, error) {
	return files.List(s.root, path, offset, limit, query)
}
func (s *Service) open(path string) (*os.Root, error) {
	if e := s.realDirectories(path, false); e != nil {
		return nil, e
	}
	return s.root.OpenRoot(filepath.Clean(path))
}
func (s *Service) realDirectories(path string, allowMissing bool) error {
	if e := files.Valid(path); e != nil {
		return e
	}
	p := "."
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		p = filepath.Join(p, part)
		st, e := s.root.Lstat(p)
		if allowMissing && errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("use actual directories, not symlink aliases")
		}
		if !st.IsDir() {
			return errors.New("path component is not a directory")
		}
	}
	return nil
}

// An explicit non-Git parent must not bypass the branch check of a nested
// checkout. Directory aliases are rejected before locating the Git boundary.
func (s *Service) checkFileTarget(t Target, path string) error {
	if e := files.Valid(path); e != nil {
		return e
	}
	parent := filepath.Join(t.Directory, filepath.Dir(path))
	if e := s.realDirectories(parent, true); e != nil {
		return e
	}
	outer, hasOuter, e := s.repo(t.Directory)
	if e != nil {
		return e
	}
	inner, hasInner, e := s.repo(parent)
	if e != nil {
		return e
	}
	if hasInner && (!hasOuter || filepath.Clean(inner) != filepath.Clean(outer)) {
		return errors.New("file belongs to a nested Git checkout; use that checkout as directory and provide its current branch")
	}
	return nil
}
func (s *Service) repo(path string) (string, bool, error) {
	p := filepath.Clean(path)
	for {
		st, e := s.root.Lstat(filepath.Join(p, ".git"))
		if e == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				return "", false, errors.New("symlink .git is not supported")
			}
			if !st.IsDir() {
				return "", false, errors.New("linked worktrees and submodules with .git files are not supported; select a normal checkout")
			}
			return p, true, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", false, e
		}
		if p == "." {
			return "", false, nil
		}
		p = filepath.Dir(p)
	}
}

type capped struct {
	b bytes.Buffer
	n int
}

func (w *capped) Write(p []byte) (int, error) {
	n := len(p)
	if w.b.Len() < w.n {
		w.b.Write(p[:min(n, w.n-w.b.Len())])
	}
	return n, nil
}
func (s *Service) git(ctx context.Context, repo string, args ...string) (string, error) {
	r, e := s.open(repo)
	if e != nil {
		return "", e
	}
	defer r.Close()
	o := &capped{n: 128 * 1024}
	er := &capped{n: 8192}
	argv := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "diff.external="}, args...)
	e = s.runner.Run(ctx, sandbox.Spec{Root: r, CWD: ".", Args: argv, Seconds: 15}, o, er)
	if e != nil {
		return "", fmt.Errorf("git: %w: %s", e, er.b.String())
	}
	if o.b.Len() == o.n {
		return "", errors.New("git output exceeded 128 KiB")
	}
	return strings.TrimSpace(o.b.String()), nil
}
func (s *Service) currentBranch(repo string) (string, error) {
	metadata, e := s.root.OpenRoot(filepath.Join(repo, ".git"))
	if e != nil {
		return "", e
	}
	defer metadata.Close()
	f, e := files.Read(metadata, "HEAD")
	if e != nil {
		return "", e
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(f.Text), "ref: refs/heads/")
	if !ok || branch == "" {
		return "", errors.New("detached HEAD; select a local branch on the PC before editing")
	}
	return branch, nil
}
func (s *Service) inspect(ctx context.Context, path string) (Info, error) {
	out := Info{Target: Target{Directory: filepath.ToSlash(filepath.Clean(path))}, Branches: []string{}}
	r, e := s.open(path)
	if e != nil {
		return out, e
	}
	r.Close()
	repo, ok, e := s.repo(path)
	if e != nil {
		return out, e
	}
	out.Git = ok
	out.GitRoot = filepath.ToSlash(repo)
	if ok && filepath.Clean(path) != filepath.Clean(repo) {
		out.Message = "Commands can modify the entire checkout: " + out.GitRoot
	}
	if !ok {
		out.Available = true
		return out, nil
	}
	out.Branch, e = s.currentBranch(repo)
	if e != nil {
		return out, e
	}
	out.Available = true
	out.Branches = []string{out.Branch}
	if s.conflict(path) {
		out.Message = "Commands active in this checkout; reads are live snapshots and branch transitions are locked."
		return out, nil
	}
	// File work remains available without command execution, including on other OSes.
	caps := s.runner.Capabilities()
	ready, hasReady := caps["execution_ready"].(bool)
	if hasReady && !ready {
		out.Message = "File work is available; full Git status and branch transitions require a working sandbox."
		return out, nil
	}
	b, e := s.git(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if e != nil {
		out.Message = "File work is available; Git status unavailable: " + e.Error()
		return out, nil
	}
	if b != "" {
		out.Branches = strings.Split(b, "\n")
	}
	status, e := s.git(ctx, repo, "status", "--porcelain=v1", "--untracked-files=normal")
	if e != nil {
		out.Message = "Git status unavailable: " + e.Error()
		return out, nil
	}
	out.Dirty = status != ""
	out.GitStatusAvailable = true
	return out, nil
}
func (s *Service) Inspect(ctx context.Context, path string) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.inspect(ctx, path)
}
func (s *Service) Selection(ctx context.Context) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out, e := s.inspect(ctx, s.selected.Directory)
	if e != nil {
		return Info{Target: s.selected, Remembered: s.remembered, Message: "Saved workspace unavailable: " + e.Error()}, nil
	}
	out.Remembered = s.remembered
	out.SessionBound = true
	if out.Branch != s.selected.Branch {
		out.Message = "Current branch changed since the previous selection. Confirm the displayed branch before editing."
	}
	return out, nil
}
func (s *Service) Select(ctx context.Context, t Target, create bool, base string) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conflict(t.Directory) {
		return Info{}, errors.New("cannot switch this checkout while a job is active")
	}
	out, e := s.inspect(ctx, t.Directory)
	if e != nil {
		return out, e
	}
	if out.Git {
		if t.Branch == "" {
			t.Branch = out.Branch
		}
		if create || t.Branch != out.Branch {
			if !out.GitStatusAvailable {
				return out, errors.New("branch transitions require Git and a working sandbox; current-branch file work is available")
			}
			if out.Dirty {
				return out, errors.New("working tree is dirty; commit or resolve changes before switching; no automatic stash")
			}
			if _, e = s.git(ctx, out.GitRoot, "check-ref-format", "--branch", t.Branch); e != nil {
				return out, e
			}
			if create {
				if base == "" {
					base = out.Branch
				}
				if strings.HasPrefix(base, "-") {
					return out, errors.New("invalid base branch")
				}
				if base == out.Branch {
					_, e = s.git(ctx, out.GitRoot, "switch", "-c", t.Branch)
				} else {
					if _, e = s.git(ctx, out.GitRoot, "show-ref", "--verify", "refs/heads/"+base); e != nil {
						return out, e
					}
					_, e = s.git(ctx, out.GitRoot, "switch", "-c", t.Branch, base)
				}
			} else {
				_, e = s.git(ctx, out.GitRoot, "show-ref", "--verify", "refs/heads/"+t.Branch)
				if e == nil {
					_, e = s.git(ctx, out.GitRoot, "switch", t.Branch)
				}
			}
			if e != nil {
				return out, e
			}
			out, e = s.inspect(ctx, t.Directory)
			if e != nil {
				return out, e
			}
		}
	} else if create || t.Branch != "" {
		return out, errors.New("directory has no Git repository")
	}
	s.selected = out.Target
	s.remembered = true
	out.Remembered = true
	out.SessionBound = true
	data, _ := json.MarshalIndent(persisted{s.rootPath, s.selected}, "", "  ")
	temp := s.state + ".tmp"
	if e = os.WriteFile(temp, data, 0600); e == nil {
		e = os.Rename(temp, s.state)
	}
	if e != nil {
		return out, fmt.Errorf("selection applied but persistence failed: %w", e)
	}
	return out, nil
}
func (s *Service) validate(ctx context.Context, t Target) (*os.Root, error) {
	if t.Directory == "" {
		return nil, errors.New("explicit directory required")
	}
	repo, ok, e := s.repo(t.Directory)
	if e != nil {
		return nil, e
	}
	if ok {
		b, e := s.currentBranch(repo)
		if e != nil {
			return nil, e
		}
		if t.Branch == "" || b != t.Branch {
			return nil, fmt.Errorf("branch conflict: current %q, requested %q; select workspace first", b, t.Branch)
		}
	} else if t.Branch != "" {
		return nil, errors.New("branch supplied for a non-Git workspace")
	}
	return s.open(t.Directory)
}
func (s *Service) Read(ctx context.Context, t Target, path string) (files.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, e := s.validate(ctx, t)
	if e != nil {
		return files.File{}, e
	}
	defer r.Close()
	if e = s.checkFileTarget(t, path); e != nil {
		return files.File{}, e
	}
	return files.Read(r, path)
}
func (s *Service) Write(ctx context.Context, t Target, path, text, revision string) (files.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exclusiveConflict(t.Directory) {
		return files.File{}, errors.New("job active in this checkout; writes are locked")
	}
	r, e := s.validate(ctx, t)
	if e != nil {
		return files.File{}, e
	}
	defer r.Close()
	if e = s.checkFileTarget(t, path); e != nil {
		return files.File{}, e
	}
	return files.Write(r, path, text, revision)
}
func (s *Service) Remove(ctx context.Context, t Target, path, revision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exclusiveConflict(t.Directory) {
		return errors.New("job active in this checkout; writes are locked")
	}
	r, e := s.validate(ctx, t)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = s.checkFileTarget(t, path); e != nil {
		return e
	}
	return files.Remove(r, path, revision)
}

// Reserve pins the chosen workspace and locks all server mutations until release.
func (s *Service) Reserve(ctx context.Context, t Target) (*os.Root, func(), error) {
	r, _, release, e := s.reserve(ctx, t, false)
	return r, release, e
}
func (s *Service) ReserveCommand(ctx context.Context, t Target) (*os.Root, string, func(), error) {
	return s.reserve(ctx, t, true)
}
func (s *Service) reserve(ctx context.Context, t Target, command bool) (*os.Root, string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !command && s.conflict(t.Directory) {
		return nil, "", nil, errors.New("another job is active in this checkout; stop it before starting a conflicting command")
	}
	r, e := s.validate(ctx, t)
	if e != nil {
		return nil, "", nil, e
	}
	prefix := "."
	if command {
		repo, ok, e := s.repo(t.Directory)
		if e != nil {
			r.Close()
			return nil, "", nil, e
		}
		if ok {
			prefix, e = filepath.Rel(repo, filepath.Clean(t.Directory))
			if e != nil {
				r.Close()
				return nil, "", nil, e
			}
			if prefix != "." {
				r.Close()
				r, e = s.open(repo)
				if e != nil {
					return nil, "", nil, e
				}
			}
		}
	}
	key, _ := filepath.Rel(s.rootPath, r.Name())
	key = filepath.Clean(key)
	for active, lease := range s.busy {
		if active == key && command && !lease.exclusive {
			continue
		}
		if overlaps(active, key) {
			r.Close()
			return nil, "", nil, errors.New("overlapping command boundary is active; use the same checkout boundary or stop it")
		}
	}
	lease := s.busy[key]
	lease.count++
	lease.exclusive = !command
	s.busy[key] = lease
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.Close()
			s.mu.Lock()
			lease := s.busy[key]
			lease.count--
			if lease.count == 0 {
				delete(s.busy, key)
			} else {
				s.busy[key] = lease
			}
			s.mu.Unlock()
		})
	}
	return r, prefix, release, nil
}

type TreeEntry struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}
type Tree struct {
	Directories []TreeEntry `json:"directories"`
	Truncated   bool        `json:"truncated"`
}

func (s *Service) Tree(path string, depth int) (Tree, error) {
	out := Tree{Directories: []TreeEntry{}}
	if depth < 1 || depth > 5 {
		depth = 3
	}
	var walk func(string, int) error
	walk = func(p string, d int) error {
		if len(out.Directories) >= 300 {
			out.Truncated = true
			return nil
		}
		out.Directories = append(out.Directories, TreeEntry{p, d})
		if d >= depth {
			return nil
		}
		page, e := s.List(p, 0, 200, "")
		if e != nil {
			return e
		}
		if page.NextOffset >= 0 {
			out.Truncated = true
		}
		for _, row := range page.Entries {
			if row.Directory && !row.Symlink && !strings.HasPrefix(row.Name, ".") && row.Name != "node_modules" && row.Name != "vendor" {
				if e = walk(row.Path, d+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	e := walk(path, 0)
	return out, e
}

// conflict treats both a checkout and any overlapping non-Git parent/child as
// one branch transition boundary. Jobs on the exact same checkout may share it.
func (s *Service) conflict(path string) bool {
	key := filepath.Clean(path)
	if repo, ok, err := s.repo(path); err == nil && ok {
		key = filepath.Clean(repo)
	}
	for active := range s.busy {
		rel, err := filepath.Rel(active, key)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			return true
		}
		rel, err = filepath.Rel(key, active)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			return true
		}
	}
	return false
}
func overlaps(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}
func (s *Service) exclusiveConflict(path string) bool {
	key := filepath.Clean(path)
	if repo, ok, err := s.repo(path); err == nil && ok {
		key = filepath.Clean(repo)
	}
	for active, lease := range s.busy {
		if lease.exclusive && overlaps(active, key) {
			return true
		}
	}
	return false
}
func (s *Service) CommandBoundary(t Target) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.validate(context.Background(), t)
	if err != nil {
		return "", err
	}
	r.Close()
	repo, ok, err := s.repo(t.Directory)
	if err != nil {
		return "", err
	}
	if ok {
		return filepath.ToSlash(repo), nil
	}
	return filepath.ToSlash(filepath.Clean(t.Directory)), nil
}

func (s *Service) RootDirectory(r *os.Root) string {
	path, err := filepath.Rel(s.rootPath, r.Name())
	if err != nil {
		return ""
	}
	return filepath.ToSlash(path)
}
