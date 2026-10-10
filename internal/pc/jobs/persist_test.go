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

type persistedRunner struct{}

func (persistedRunner) Capabilities() map[string]any { return map[string]any{} }
func (persistedRunner) Run(_ context.Context, _ sandbox.Spec, out, er io.Writer) error {
	_, _ = out.Write([]byte("persisted stdout\n"))
	_, _ = er.Write([]byte("persisted stderr\n"))
	return nil
}

func TestSessionJobHistorySurvivesManagerRestart(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	state := filepath.Join(base, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	runner := persistedRunner{}
	ws, err := workspace.New(root, state, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	info, err := ws.SessionSelection(context.Background(), "chat-a", true)
	if err != nil || !info.SessionBound {
		t.Fatal(info, err)
	}

	m := New(context.Background(), ws, runner, 10)
	request := Request{Target: info.Target, Args: []string{"example"}, Purpose: "run", Seconds: 10}
	v, _, err := m.PrepareSession("chat-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartSession(context.Background(), "chat-a", v.ID, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		v, err = m.GetSession("chat-a", info.Target, v.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	m.Close()

	restarted := New(context.Background(), ws, runner, 10)
	defer restarted.Close()
	history, err := restarted.ListSession("chat-a", info.Target)
	if err != nil || len(history) != 1 || history[0].ID != v.ID || history[0].Status != "succeeded" {
		t.Fatalf("persisted history missing: %+v %v", history, err)
	}
	loaded, err := restarted.GetSession("chat-a", info.Target, v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Output.Records) != 2 || loaded.Output.Records[0].Text != "persisted stdout" || loaded.Output.Records[1].Text != "persisted stderr" {
		t.Fatalf("persisted console missing: %+v", loaded.Output)
	}
	if _, err := restarted.GetSession("chat-b", info.Target, v.ID, 0); err == nil {
		t.Fatal("persisted job leaked across chat sessions")
	}
}
