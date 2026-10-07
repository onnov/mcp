package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onnov/mcp/internal/pc/workspace"
)

func TestPurposeCannotChangeCapabilities(t *testing.T) {
	for _, purpose := range []string{"build", "test", "inspect", "smoke", "run", "git", "gh"} {
		r := Request{Purpose: purpose, Args: []string{"bash", "-lc", "rm -rf generated"}}
		if NeedsApproval(r) {
			t.Fatal("local development grant unexpectedly depends on purpose", purpose)
		}
		r.Network = true
		if !NeedsApproval(r) {
			t.Fatal("network approval bypassed", purpose)
		}
		r.Network = false
		r.Credential = true
		if !NeedsApproval(r) {
			t.Fatal("credential approval bypassed", purpose)
		}
	}
}

func TestCancelAllInvalidatesApprovalsAndStopsActiveJob(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	state := filepath.Join(base, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	runner := &blockingRunner{make(chan struct{}, 1)}
	ws, err := workspace.New(root, state, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	m := New(context.Background(), ws, runner, 10)
	defer m.Close()
	request := Request{Target: workspace.Target{Directory: "."}, Args: []string{"script"}, Purpose: "run", Seconds: 10}
	active, _, err := m.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Start(context.Background(), active.ID, ""); err != nil {
		t.Fatal(err)
	}
	pending, nonce, err := m.PrepareApproval(request)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := m.CancelAll()
	if len(cancelled) != 2 {
		t.Fatal(cancelled)
	}
	if _, err = m.Start(context.Background(), pending.ID, nonce); err == nil {
		t.Fatal("pending approval replay after stop-all")
	}
	deadline := time.Now().Add(time.Second)
	for {
		v, err := m.Get(active.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active job not stopped")
		}
		time.Sleep(time.Millisecond)
	}
	// Emergency stop does not permanently disable development.
	next, _, err := m.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Start(context.Background(), next.ID, ""); err != nil {
		t.Fatal(err)
	}
	m.CancelAll()
}

func TestOperatorCanRequireAllCommands(t *testing.T) {
	m := &Manager{ConfirmCommands: true}
	for _, purpose := range []string{"inspect", "test", "build", "run"} {
		if !m.NeedsApproval(Request{Purpose: purpose}) {
			t.Fatal("operator confirmation policy bypassed")
		}
	}
}

func TestMiddleOutputPagination(t *testing.T) {
	out := &Output{}
	w := out.Writer("stdout")
	for i := 0; i < 300; i++ {
		w.Write([]byte("line\n"))
	}
	w.Close()
	p := out.Page(100, 20)
	if len(p.Records) != 20 || p.Records[0].Sequence != 101 || p.Cursor != 120 || p.Evicted != 0 {
		t.Fatal(p)
	}
}

func TestCredentialGitCommandAllowlist(t *testing.T) {
	for _, args := range [][]string{
		{"git", "pull"},
		{"git", "push", "origin", "main"},
		{"git", "fetch"},
		{"git", "clone", "git@github.com:owner/repo.git"},
		{"git", "ls-remote", "origin"},
	} {
		if !allowedCredentialGit(args) {
			t.Fatal("allowed credential git command rejected", args)
		}
	}
	for _, args := range [][]string{
		{"git"},
		{"git", "-c", "core.sshCommand=sh"},
		{"git", "hash-object", "/run/pc-mcp-ssh-key"},
		{"git", "config", "--get", "remote.origin.url"},
		{"bash", "-lc", "git pull"},
	} {
		if allowedCredentialGit(args) {
			t.Fatal("unsafe credential git command accepted", args)
		}
	}
}
