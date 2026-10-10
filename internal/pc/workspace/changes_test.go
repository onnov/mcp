package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChangeSummaryIncludesCommittedWorkingAndUntrackedChanges(t *testing.T) {
	s := testService(t, true)
	ctx := context.Background()
	repo := filepath.Join(s.rootPath, "project")

	if err := os.WriteFile(filepath.Join(repo, "mod.txt"), []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "del.txt"), []byte("x\ny\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git(ctx, "project", "add", "mod.txt", "del.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git(ctx, "project", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixtures"); err != nil {
		t.Fatal(err)
	}
	feature, err := s.Select(ctx, Target{"project", "feature"}, true, "main")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repo, "mod.txt"), []byte("a\nc\nd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git(ctx, "project", "add", "mod.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git(ctx, "project", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "modify"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "del.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("n1\nn2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := s.ChangeSummary(ctx, feature.Target, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 4 || got.Deletions != 3 {
		t.Fatalf("totals = +%d -%d; want +4 -3: %#v", got.Additions, got.Deletions, got)
	}
	want := map[string]ChangeFile{
		"del.txt": {Path: "del.txt", Status: "deleted", Deletions: 2},
		"mod.txt": {Path: "mod.txt", Status: "modified", Additions: 2, Deletions: 1},
		"new.txt": {Path: "new.txt", Status: "created", Additions: 2},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %#v", got.Files)
	}
	for _, f := range got.Files {
		w, ok := want[f.Path]
		if !ok || f != w {
			t.Fatalf("file %q = %#v; want %#v", f.Path, f, w)
		}
	}
}

func TestChangeSummaryRejectsStaleBranchAndInvalidBase(t *testing.T) {
	s := testService(t, true)
	ctx := context.Background()
	if _, err := s.ChangeSummary(ctx, Target{"project", "other"}, "main"); err == nil {
		t.Fatal("stale branch accepted")
	}
	if _, err := s.ChangeSummary(ctx, Target{"project", "main"}, "--output=/tmp/x"); err == nil {
		t.Fatal("option-like base accepted")
	}
}
