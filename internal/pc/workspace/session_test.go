package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatScopedWorkspaceBindingPersistsAndCreatesContext(t *testing.T) {
	s := testService(t, true)
	ctx := context.Background()
	if _, err := s.Select(ctx, Target{Directory: "project", Branch: "main"}, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.rootPath, "other"), 0700); err != nil {
		t.Fatal(err)
	}

	before, err := s.SessionSelection(ctx, "chat-a", false)
	if err != nil || before.SessionBound {
		t.Fatalf("new chat unexpectedly bound: %+v %v", before, err)
	}
	bound, err := s.SessionSelection(ctx, "chat-a", true)
	if err != nil || !bound.SessionBound || bound.Directory != "project" || bound.Branch != "main" {
		t.Fatalf("default was not bound to chat-a: %+v %v", bound, err)
	}

	sessionFile := filepath.Join(s.rootPath, "project", ContextDirName, "sessions", SessionKey("chat-a"), "session.json")
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatal("missing persisted project context", err)
	}
	ignore, err := os.ReadFile(filepath.Join(s.rootPath, "project", ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), ContextDirName+"/") {
		t.Fatalf("context directory was not added to .gitignore: %q %v", ignore, err)
	}
	status, err := s.git(ctx, "project", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status, ContextDirName) {
		t.Fatalf("context directory leaked into git status: %q", status)
	}

	other, err := s.SelectSession(ctx, "chat-b", Target{Directory: "other"}, false, "")
	if err != nil || !other.SessionBound || other.Directory != "other" {
		t.Fatalf("chat-b selection failed: %+v %v", other, err)
	}
	a, err := s.SessionSelection(ctx, "chat-a", false)
	if err != nil || a.Directory != "project" || a.Branch != "main" {
		t.Fatalf("chat-a changed with chat-b/global default: %+v %v", a, err)
	}
	b, err := s.SessionSelection(ctx, "chat-b", false)
	if err != nil || b.Directory != "other" || b.Branch != "" {
		t.Fatalf("chat-b binding missing: %+v %v", b, err)
	}
	if err := s.ValidateSessionTarget("chat-a", Target{Directory: "other"}); err == nil {
		t.Fatal("cross-chat target accepted")
	}

	reloaded, err := New(s.rootPath, filepath.Dir(s.state), gitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	a2, err := reloaded.SessionSelection(ctx, "chat-a", false)
	if err != nil || !a2.SessionBound || a2.Directory != "project" || a2.Branch != "main" {
		t.Fatalf("chat-a binding did not survive restart: %+v %v", a2, err)
	}
	b2, err := reloaded.SessionSelection(ctx, "chat-b", false)
	if err != nil || !b2.SessionBound || b2.Directory != "other" {
		t.Fatalf("chat-b binding did not survive restart: %+v %v", b2, err)
	}
}

func TestContextPrunesOldJobSnapshotsByCount(t *testing.T) {
	s := testService(t, false)
	ctx := context.Background()
	info, err := s.SelectSession(ctx, "chat-prune", Target{Directory: "project"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, jobsDir, err := s.ContextPaths(ctx, "chat-prune", info.Target)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 201; i++ {
		path := filepath.Join(jobsDir, fmt.Sprintf("%03d.json", i))
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneContext(ctx, "chat-prune", info.Target); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 150 {
		t.Fatalf("pruned job count = %d; want 150", len(entries))
	}
	if _, err := os.Stat(filepath.Join(jobsDir, "000.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest snapshot was not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(jobsDir, "200.json")); err != nil {
		t.Fatalf("newest snapshot was pruned: %v", err)
	}
}

func TestContextRejectsSymlinkedInternalDirectory(t *testing.T) {
	s := testService(t, false)
	ctx := context.Background()
	key := SessionKey("chat-link")
	contextRoot := filepath.Join(s.rootPath, "project", ContextDirName)
	if err := os.Mkdir(contextRoot, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(contextRoot, "sessions")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s.mu.Lock()
	_, _, err := s.ensureContextLocked(ctx, key, Target{Directory: "project"})
	s.mu.Unlock()
	if err == nil {
		t.Fatal("symlinked .pcctx/sessions accepted")
	}
}
