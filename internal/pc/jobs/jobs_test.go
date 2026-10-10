package jobs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/workspace"
)

type blockingRunner struct{ started chan struct{} }

func (r *blockingRunner) Capabilities() map[string]any { return map[string]any{} }
func (r *blockingRunner) Run(ctx context.Context, s sandbox.Spec, out, er io.Writer) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	out.Write([]byte("started\n"))
	<-ctx.Done()
	er.Write([]byte("cancelled\n"))
	return ctx.Err()
}
func TestApprovalNonceAndCancellation(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	state := filepath.Join(dir, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	r := &blockingRunner{make(chan struct{}, 1)}
	ws, e := workspace.New(root, state, r)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	m := New(context.Background(), ws, r, 10)
	defer m.Close()
	q := Request{Target: workspace.Target{Directory: "."}, Args: []string{"example"}, Purpose: "run", Network: true, Seconds: 10}
	v, nonce, e := m.Prepare(q)
	if e != nil || v.Status != "awaiting_approval" || nonce == "" {
		t.Fatal(v, nonce, e)
	}
	if _, e = m.Start(context.Background(), v.ID, ""); e == nil {
		t.Fatal("approval bypassed")
	}
	if _, e = m.Start(context.Background(), v.ID, "wrong"); e == nil {
		t.Fatal("bad nonce accepted")
	}
	if _, e = m.Start(context.Background(), v.ID, nonce); e != nil {
		t.Fatal(e)
	}
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	if _, e = m.Start(context.Background(), v.ID, nonce); e == nil {
		t.Fatal("nonce replay accepted")
	}
	if _, e = ws.Write(context.Background(), q.Target, "file", "x", "new"); e != nil {
		t.Fatal("running development command blocked live edit", e)
	}
	m.Cancel(v.ID)
	deadline := time.Now().Add(time.Second)
	for {
		v, e = m.Get(v.ID, 0)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
	history := m.List()
	if len(history) != 1 || len(history[0].Output.Head) != 0 || len(history[0].Output.Tail) != 0 || history[0].Output.Cursor != 0 {
		t.Fatal("history should not advance console cursor", history)
	}
	if len(v.Output.Head) != 2 {
		t.Fatal("missing captured output", v.Output)
	}
}
func TestNetworkAndCredentialRequireApproval(t *testing.T) {
	if !NeedsApproval(Request{Purpose: "test", Network: true}) || !NeedsApproval(Request{Purpose: "inspect", Credential: true}) || NeedsApproval(Request{Purpose: "build"}) {
		t.Fatal("approval policy mismatch")
	}
}

func TestCancelAllWithoutChatKeepsOtherChatsRunning(t *testing.T) {
	dir := t.TempDir()
	root, state := filepath.Join(dir, "root"), filepath.Join(dir, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	r := &blockingRunner{make(chan struct{}, 2)}
	ws, e := workspace.New(root, state, r)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	if _, e = ws.SessionSelection(context.Background(), "chat-a", true); e != nil {
		t.Fatal(e)
	}
	m := New(context.Background(), ws, r, 10)
	defer m.Close()
	start := func(session string) string {
		t.Helper()
		v, _, e := m.PrepareSession(session, Request{Target: workspace.Target{Directory: "."}, Args: []string{"example"}, Purpose: "run", Seconds: 10})
		if e == nil {
			_, e = m.StartSession(context.Background(), session, v.ID, "")
		}
		if e != nil {
			t.Fatal(e)
		}
		select {
		case <-r.started:
		case <-time.After(time.Second):
			t.Fatal("not started")
		}
		return v.ID
	}
	other, shared := start("chat-a"), start("")
	if rows := m.CancelAllSession(""); len(rows) != 1 || rows[0].ID != shared {
		t.Fatal("cancel-all without a chat must stop only shared-session jobs", rows)
	}
	time.Sleep(50 * time.Millisecond)
	if v, e := m.GetSession("chat-a", workspace.Target{Directory: "."}, other, 0); e != nil || v.Status != "running" {
		t.Fatal("another chat's job was stopped", v.Status, e)
	}
}
