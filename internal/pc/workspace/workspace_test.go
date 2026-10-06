package workspace

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onnov/mcp/internal/pc/sandbox"
)

// This test adapter only executes fixed git argv against temporary test repos.
// Production always uses bubblewrap, never this adapter.
type gitRunner struct{}

func (gitRunner) Capabilities() map[string]any { return map[string]any{"git": true} }
func (gitRunner) Run(ctx context.Context, s sandbox.Spec, out, er io.Writer) error {
	cmd := exec.CommandContext(ctx, s.Args[0], s.Args[1:]...)
	cmd.Dir = s.Root.Name()
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}
	cmd.Stdout = out
	cmd.Stderr = er
	return cmd.Run()
}
func testService(t *testing.T, git bool) *Service {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "projects")
	state := filepath.Join(dir, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	os.Mkdir(filepath.Join(root, "project"), 0700)
	s, e := New(root, state, gitRunner{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if git {
		if _, e = s.git(context.Background(), "project", "init", "-b", "main"); e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(root, "project", "file"), []byte("one"), 0644)
		s.git(context.Background(), "project", "add", "file")
		if _, e = s.git(context.Background(), "project", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial"); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func TestBranchSwitchDirtyGuardAndPersistence(t *testing.T) {
	s := testService(t, true)
	ctx := context.Background()
	out, e := s.Select(ctx, Target{"project", "feature"}, true, "main")
	if e != nil {
		t.Fatal(e)
	}
	if out.Branch != "feature" {
		t.Fatal(out)
	}
	r, e := s.Read(ctx, Target{"project", "main"}, "file")
	if e == nil {
		t.Fatal("stale branch accepted", r)
	}
	r, e = s.Read(ctx, out.Target, "file")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Write(ctx, out.Target, "file", "changed", r.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Select(ctx, Target{"project", "main"}, false, ""); e == nil {
		t.Fatal("dirty branch switch accepted")
	}
	other, e := New(s.rootPath, filepath.Dir(s.state), gitRunner{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	saved, e := other.Selection(ctx)
	if e != nil || saved.Directory != "project" || saved.Branch != "feature" {
		t.Fatalf("selection was not persisted %+v %v", saved, e)
	}
}
func TestNoGitLeaseAndExplicitTarget(t *testing.T) {
	s := testService(t, false)
	ctx := context.Background()
	out, e := s.Select(ctx, Target{"project", ""}, false, "")
	if e != nil || out.Git {
		t.Fatal(out, e)
	}
	r, release, e := s.Reserve(ctx, out.Target)
	if e != nil {
		t.Fatal(e)
	}
	if r == nil {
		t.Fatal("missing root")
	}
	if _, e = s.Write(ctx, out.Target, "new", "text", "new"); e == nil {
		t.Fatal("write allowed while job runs")
	}
	if _, e = s.Select(ctx, out.Target, false, ""); e == nil {
		t.Fatal("selection allowed while job runs")
	}
	release()
	release()
	if _, e = s.Write(ctx, out.Target, "new", "text", "new"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read(ctx, Target{"../", ""}, "secret"); e == nil {
		t.Fatal("directory escape")
	}
}
func TestGitFilesWithoutCommandSandbox(t *testing.T) {
	s := testService(t, true)
	s.runner = &sandbox.Engine{MaxSeconds: 10}
	ctx := context.Background()
	info, e := s.Select(ctx, Target{"project", "main"}, false, "")
	if e != nil || !info.Available || info.GitStatusAvailable || info.Branch != "main" {
		t.Fatal(info, e)
	}
	f, e := s.Read(ctx, info.Target, "file")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Write(ctx, info.Target, "file", "changed", f.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Select(ctx, Target{"project", "feature"}, true, "main"); e == nil {
		t.Fatal("branch transition bypassed sandbox")
	}
}
func TestCommandFromNestedDirectoryPinsCheckout(t *testing.T) {
	s := testService(t, true)
	os.MkdirAll(filepath.Join(s.rootPath, "project", "nested"), 0700)
	r, prefix, release, e := s.ReserveCommand(context.Background(), Target{"project/nested", "main"})
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if prefix != "nested" || filepath.Clean(r.Name()) != filepath.Join(s.rootPath, "project") {
		t.Fatalf("incorrect checkout mount: %s %s", r.Name(), prefix)
	}
}
func TestProjectSymlinkAliasRejected(t *testing.T) {
	s := testService(t, false)
	os.Symlink("project", filepath.Join(s.rootPath, "alias"))
	if _, e := s.Select(context.Background(), Target{"alias", ""}, false, ""); e == nil {
		t.Fatal("symlink project alias selected")
	}
	if _, e := s.Write(context.Background(), Target{"alias", ""}, "file", "x", "new"); e == nil {
		t.Fatal("symlink alias allowed writes")
	}
}
