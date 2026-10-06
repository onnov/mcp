package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/workspace"
)

func TestMCPToolsAndPrivateApprovalMetadata(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "root")
	state := filepath.Join(d, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	engine := &sandbox.Engine{MaxSeconds: 10}
	ws, e := workspace.New(root, state, engine)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx, ws, engine, 10)
	defer jm.Close()
	server := New(ws, jm)
	st, ct := mcp.NewInMemoryTransports()
	go server.Run(ctx, st)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	list, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Tools) != 16 {
		t.Fatalf("expected 16 tools, got %d", len(list.Tools))
	}
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := call("pc_write_file", map[string]any{"directory": ".", "branch": "", "path": "a.txt", "text": "one", "expected_revision": "new"})
	if r.IsError {
		t.Fatal(r.Content)
	}
	r = call("pc_read_file", map[string]any{"directory": ".", "branch": "", "path": "a.txt"})
	b, _ := json.Marshal(r.StructuredContent)
	if !json.Valid(b) {
		t.Fatal("no structured content")
	}
	r = call("pc_start_job", map[string]any{"directory": ".", "branch": "", "args": []string{"echo", "run"}, "purpose": "run", "seconds": 1})
	if !r.IsError {
		t.Fatal("run bypassed confirmation card")
	}
	r = call("pc_request_run", map[string]any{"directory": ".", "branch": "", "args": []string{"echo", "run"}, "purpose": "run", "seconds": 1})
	if r.IsError {
		t.Fatal(r.Content)
	}
	nonce, ok := r.Meta["approval_nonce"].(string)
	if !ok || nonce == "" {
		t.Fatal("missing private nonce")
	}
	b, _ = json.Marshal(r.StructuredContent)
	var v jobs.View
	json.Unmarshal(b, &v)
	if v.Status != "awaiting_approval" {
		t.Fatal(v)
	}
	if strings.Contains(string(b), nonce) {
		t.Fatal("nonce in model result")
	}
	r = call("pc_approve_run", map[string]any{"id": v.ID, "approval_nonce": "wrong"})
	if !r.IsError {
		t.Fatal("invalid nonce accepted")
	}
	for _, tool := range list.Tools {
		if tool.Name == "pc_approve_run" {
			meta := tool.Meta["ui"].(map[string]any)
			visibility := meta["visibility"].([]any)
			if len(visibility) != 1 || visibility[0] != "app" {
				t.Fatal("approval exposed to model")
			}
		}
	}
}
