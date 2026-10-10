package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const ContextDirName = ".pcctx"

type sessionBinding struct {
	Target
	Updated time.Time `json:"updated"`
}

type persistedSessions struct {
	Root     string                    `json:"root"`
	Sessions map[string]sessionBinding `json:"sessions"`
}

type contextSession struct {
	Version    int       `json:"version"`
	SessionKey string    `json:"session_key"`
	Directory  string    `json:"directory"`
	Branch     string    `json:"branch"`
	Updated    time.Time `json:"updated"`
}

func SessionKey(session string) string {
	if session == "" {
		return "legacy"
	}
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:16])
}

func (s *Service) loadSessions() error {
	s.sessions = map[string]sessionBinding{}
	b, err := os.ReadFile(s.sessionsState)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var p persistedSessions
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("invalid chat workspace state: %w", err)
	}
	if p.Root != s.rootPath {
		return nil
	}
	for key, binding := range p.Sessions {
		if filesValidTarget(binding.Target) == nil {
			s.sessions[key] = binding
		}
	}
	return nil
}

func filesValidTarget(t Target) error {
	if t.Directory == "" {
		return errors.New("empty directory")
	}
	if filepath.IsAbs(t.Directory) || !filepath.IsLocal(t.Directory) {
		return errors.New("invalid directory")
	}
	if strings.ContainsAny(t.Branch, "\x00\r\n") {
		return errors.New("invalid branch")
	}
	return nil
}

func (s *Service) persistSessionsLocked() error {
	data, err := json.MarshalIndent(persistedSessions{Root: s.rootPath, Sessions: s.sessions}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.sessionsState), 0700); err != nil {
		return err
	}
	tmp := s.sessionsState + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.sessionsState)
}

func (s *Service) selectionLocked(ctx context.Context, target Target, remembered bool, sessionBound bool) (Info, error) {
	out, err := s.inspect(ctx, target.Directory)
	if err != nil {
		return Info{Target: target, Remembered: remembered, SessionBound: sessionBound, Message: "Saved workspace unavailable: " + err.Error()}, nil
	}
	actual := out.Target
	out.Target = target
	out.Remembered = remembered
	out.SessionBound = sessionBound
	if out.Git && actual.Branch != target.Branch {
		out.Message = fmt.Sprintf("Current branch is %q, while this chat is bound to %q. Confirm or change the workspace before editing.", actual.Branch, target.Branch)
	}
	return out, nil
}

// SessionSelection returns the workspace bound to this ChatGPT session.
// If bindDefault is true and the chat is new, the global last selection is
// accepted as this chat's default immediately. That makes closing the picker
// without interaction equivalent to accepting the prefilled selection.
func (s *Service) SessionSelection(ctx context.Context, session string, bindDefault bool) (Info, error) {
	if session == "" {
		return s.Selection(ctx)
	}
	key := SessionKey(session)
	s.mu.Lock()
	defer s.mu.Unlock()

	if binding, ok := s.sessions[key]; ok {
		return s.selectionLocked(ctx, binding.Target, true, true)
	}
	out, err := s.selectionLocked(ctx, s.selected, s.remembered, false)
	if err != nil || !bindDefault || !out.Available {
		if err == nil && !bindDefault {
			out.Message = "No workspace is bound to this chat. Open the workspace picker before doing project work."
		}
		return out, err
	}

	// Bind the actual branch shown by inspect, not a stale branch persisted by
	// another chat or external Git process.
	actual, err := s.inspect(ctx, s.selected.Directory)
	if err != nil {
		return out, err
	}
	binding := sessionBinding{Target: actual.Target, Updated: time.Now().UTC()}
	s.sessions[key] = binding
	if err := s.persistSessionsLocked(); err != nil {
		delete(s.sessions, key)
		return out, fmt.Errorf("chat workspace selected but persistence failed: %w", err)
	}
	if _, _, err := s.ensureContextLocked(ctx, key, binding.Target); err != nil {
		delete(s.sessions, key)
		_ = s.persistSessionsLocked()
		return out, fmt.Errorf("chat workspace context initialization failed: %w", err)
	}
	return s.selectionLocked(ctx, binding.Target, true, true)
}

// BindDefault binds a chat that has no workspace yet to the last used
// workspace (the global default), so every chat works in a definite directory
// and branch from its first call. It reports whether it created the binding.
func (s *Service) BindDefault(ctx context.Context, session string) (bool, error) {
	if session == "" {
		return false, nil
	}
	key := SessionKey(session)
	s.mu.Lock()
	_, bound := s.sessions[key]
	s.mu.Unlock()
	if bound {
		return false, nil
	}
	info, err := s.SessionSelection(ctx, session, true)
	return err == nil && info.SessionBound, err
}

func (s *Service) SelectSession(ctx context.Context, session string, t Target, create bool, base string) (Info, error) {
	out, err := s.Select(ctx, t, create, base)
	if err != nil || session == "" {
		return out, err
	}
	key := SessionKey(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := sessionBinding{Target: out.Target, Updated: time.Now().UTC()}
	s.sessions[key] = binding
	if err := s.persistSessionsLocked(); err != nil {
		return out, fmt.Errorf("workspace switched but chat binding persistence failed: %w", err)
	}
	if _, _, err := s.ensureContextLocked(ctx, key, binding.Target); err != nil {
		delete(s.sessions, key)
		_ = s.persistSessionsLocked()
		return out, fmt.Errorf("workspace switched but chat context initialization failed: %w", err)
	}
	out.SessionBound = true
	return out, nil
}

func (s *Service) ValidateSessionTarget(session string, t Target) error {
	if session == "" {
		return nil
	}
	key := SessionKey(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.sessions[key]
	if !ok {
		return errors.New("no workspace is bound to this chat; call pc_open_workspace_picker before project work")
	}
	if filepath.Clean(binding.Directory) != filepath.Clean(t.Directory) || binding.Branch != t.Branch {
		return fmt.Errorf("chat workspace is %s @ %s, requested %s @ %s; open the picker to change this chat workspace",
			binding.Directory, binding.Branch, t.Directory, t.Branch)
	}
	return nil
}

func (s *Service) contextRootLocked(t Target) (string, bool, error) {
	repo, git, err := s.repo(t.Directory)
	if err != nil {
		return "", false, err
	}
	if git {
		return filepath.Clean(repo), true, nil
	}
	return filepath.Clean(t.Directory), false, nil
}

func ignoredContextLine(line string) bool {
	line = strings.TrimSpace(line)
	return line == ContextDirName || line == ContextDirName+"/" || line == "/"+ContextDirName || line == "/"+ContextDirName+"/"
}

func (s *Service) ensureGitignoreLocked(root string) error {
	path := filepath.Join(root, ".gitignore")
	if st, err := s.root.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return errors.New(".gitignore must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	abs := filepath.Join(s.rootPath, path)
	data, err := os.ReadFile(abs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if ignoredContextLine(line) {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte(ContextDirName+"/\n")...)
	tmp := abs + ".pcctx.tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

func (s *Service) ensureRealDirLocked(path string) error {
	if st, err := s.root.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return fmt.Errorf("%s must be a real directory", filepath.ToSlash(path))
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.root.Mkdir(path, 0700); err != nil {
		return err
	}
	st, err := s.root.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("%s must be a real directory", filepath.ToSlash(path))
	}
	return nil
}

func (s *Service) ensureContextLocked(ctx context.Context, sessionKey string, t Target) (string, string, error) {
	if err := s.realDirectories(t.Directory, false); err != nil {
		return "", "", err
	}
	root, git, err := s.contextRootLocked(t)
	if err != nil {
		return "", "", err
	}
	if git {
		if err := s.ensureGitignoreLocked(root); err != nil {
			return "", "", fmt.Errorf("cannot add %s to .gitignore: %w", ContextDirName, err)
		}
	}
	contextRel := filepath.Join(root, ContextDirName)
	sessionsRel := filepath.Join(contextRel, "sessions")
	sessionRel := filepath.Join(sessionsRel, sessionKey)
	jobsRel := filepath.Join(sessionRel, "jobs")
	for _, dir := range []string{contextRel, sessionsRel, sessionRel, jobsRel} {
		if err := s.ensureRealDirLocked(dir); err != nil {
			return "", "", err
		}
	}
	baseAbs := filepath.Join(s.rootPath, contextRel)
	sessionAbs := filepath.Join(s.rootPath, sessionRel)
	jobsAbs := filepath.Join(s.rootPath, jobsRel)
	meta := contextSession{Version: 1, SessionKey: sessionKey, Directory: t.Directory, Branch: t.Branch, Updated: time.Now().UTC()}
	data, _ := json.MarshalIndent(meta, "", "  ")
	tmp := filepath.Join(sessionAbs, "session.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, filepath.Join(sessionAbs, "session.json")); err != nil {
		return "", "", err
	}
	return baseAbs, jobsAbs, nil
}

func (s *Service) ContextPaths(ctx context.Context, session string, t Target) (string, string, error) {
	if err := s.ValidateSessionTarget(session, t); err != nil {
		return "", "", err
	}
	return s.ContextPathsForTarget(ctx, session, t)
}

// ContextPathsForTarget is used for terminal job persistence. A chat may have
// selected another project while an older job was still finishing, so history
// must be written back to the job's original project rather than the new binding.
func (s *Service) ContextPathsForTarget(ctx context.Context, session string, t Target) (string, string, error) {
	key := SessionKey(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureContextLocked(ctx, key, t)
}

type contextFile struct {
	path string
	size int64
	mod  time.Time
}

// PruneContext bounds persisted command history for the whole project. Session
// metadata is retained; only oldest completed job snapshots are removed.
func (s *Service) PruneContext(ctx context.Context, session string, t Target) error {
	base, _, err := s.ContextPathsForTarget(ctx, session, t)
	if err != nil {
		return err
	}
	const high = int64(64 << 20)
	const low = int64(48 << 20)
	const maxJobs = 200
	const keepJobs = 150
	files := []contextFile{}
	var total int64
	root := filepath.Join(base, "sessions")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil || d.IsDir() || filepath.Ext(path) != ".json" || filepath.Base(filepath.Dir(path)) != "jobs" {
			return nil
		}
		info, er := d.Info()
		if er != nil {
			return nil
		}
		files = append(files, contextFile{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= high && len(files) <= maxJobs {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for len(files) > keepJobs || total > low {
		f := files[0]
		files = files[1:]
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
	return nil
}
