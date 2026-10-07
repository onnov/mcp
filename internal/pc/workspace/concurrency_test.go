package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentWorkspaceLeasesAndOverlap(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	state := filepath.Join(base, "state")
	os.MkdirAll(filepath.Join(root, "a", "nested"), 0700)
	os.MkdirAll(filepath.Join(root, "b"), 0700)
	os.Mkdir(state, 0700)
	s, err := New(root, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, releaseA, err := s.Reserve(context.Background(), Target{Directory: "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	if _, _, err = s.Reserve(context.Background(), Target{Directory: "a/nested"}); err == nil {
		t.Fatal("overlapping directory escaped lease")
	}
	if _, _, err = s.Reserve(context.Background(), Target{Directory: "."}); err == nil {
		t.Fatal("parent directory escaped lease")
	}
	_, releaseB, err := s.Reserve(context.Background(), Target{Directory: "b"})
	if err != nil {
		t.Fatal("independent project blocked", err)
	}
	releaseB()
	if _, err = s.Write(context.Background(), Target{Directory: "b"}, "file", "editable", "new"); err != nil {
		t.Fatal("independent file edit blocked", err)
	}
	os.WriteFile(filepath.Join(root, "a", "snapshot"), []byte("data"), 0600)
	if _, err = s.Read(context.Background(), Target{Directory: "a"}, "snapshot"); err != nil {
		t.Fatal("snapshot read blocked", err)
	}
}

func TestConcurrentCommandsAllowLiveEditsAndKeepBranchBoundary(t *testing.T) {
	s := testService(t, false)
	ctx := context.Background()
	target := Target{Directory: "project"}
	_, _, releaseA, err := s.ReserveCommand(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	_, _, releaseB, err := s.ReserveCommand(ctx, target)
	if err != nil {
		t.Fatal("server and smoke cannot share checkout", err)
	}
	defer releaseB()
	file, err := s.Write(ctx, target, "live.txt", "hot reload", "new")
	if err != nil {
		t.Fatal("live edit blocked", err)
	}
	if err = s.Remove(ctx, target, "live.txt", file.Revision); err != nil {
		t.Fatal("live delete blocked", err)
	}
	if _, err = s.Select(ctx, target, false, ""); err == nil {
		t.Fatal("branch lease was not held")
	}
	releaseA()
	if _, err = s.Select(ctx, target, false, ""); err == nil {
		t.Fatal("first command released second command's branch lease")
	}
	if _, _, _, err = s.ReserveCommand(ctx, Target{Directory: "."}); err == nil {
		t.Fatal("overlapping broader command scope accepted")
	}
	releaseB()
	if _, err = s.Select(ctx, target, false, ""); err != nil {
		t.Fatal("last command did not release checkout", err)
	}
}
