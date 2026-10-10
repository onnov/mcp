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
	"github.com/onnov/mcp/internal/pc/ui"
	"github.com/onnov/mcp/internal/pc/workspace"
)

func TestMCPToolsAndPrivateApprovalMetadata(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "root")
	state := filepath.Join(d, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	const deep = "AI/project/a/b/c/d/e/f"
	if e := os.MkdirAll(filepath.Join(root, deep, "child"), 0700); e != nil {
		t.Fatal(e)
	}
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
	if len(list.Tools) != 19 {
		t.Fatalf("expected 19 tools, got %d", len(list.Tools))
	}
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	callChat := func(chat, name string, args any) *mcp.CallToolResult {
		t.Helper()
		r, e := session.CallTool(ctx, &mcp.CallToolParams{Meta: mcp.Meta{"openai/session": chat}, Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	foundChangeSummary := false
	for _, tool := range list.Tools {
		if tool.Name == "pc_git_change_summary" {
			foundChangeSummary = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Fatal("change summary must be read-only")
			}
		}
		if tool.Name == "pc_open_workspace_picker" || tool.Name == "pc_list_directory" || tool.Name == "pc_directory_tree" {
			meta := tool.Meta["ui"].(map[string]any)
			if meta["resourceUri"] != ui.PickerURI || tool.Meta["openai/outputTemplate"] != ui.PickerURI {
				t.Fatal("directory browsing tool missing interactive picker", tool.Name)
			}
		}
		if tool.Name == "pc_start_job" || tool.Name == "pc_request_run" {
			meta := tool.Meta["ui"].(map[string]any)
			if meta["resourceUri"] != ui.ApprovalURI || tool.Meta["openai/outputTemplate"] != ui.ApprovalURI {
				t.Fatal("command tool missing current approval UI", tool.Name, meta["resourceUri"], tool.Meta["openai/outputTemplate"])
			}
		}
	}
	if !foundChangeSummary {
		t.Fatal("pc_git_change_summary missing")
	}
	for _, name := range []string{"pc_list_directory", "pc_directory_tree"} {
		r := call(name, map[string]any{"path": "AI"})
		if r.IsError || r.Meta["pc_browser_path"] != "AI" {
			t.Fatal("directory card lost requested starting path", name, r)
		}
	}
	decodePicker := func(args any) Picker {
		t.Helper()
		r := call("pc_open_workspace_picker", args)
		if r.IsError {
			t.Fatal(r.Content)
		}
		b, _ := json.Marshal(r.StructuredContent)
		var p Picker
		if e := json.Unmarshal(b, &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	if p := decodePicker(map[string]any{"path": deep}); p.BrowserPath != deep || len(p.Directories.Entries) != 1 || p.Directories.Entries[0].Name != "child" {
		t.Fatal("picker cannot open a directory beyond the text tree depth", p)
	}
	if _, e := ws.Select(ctx, workspace.Target{Directory: deep}, false, ""); e != nil {
		t.Fatal(e)
	}
	if p := decodePicker(map[string]any{}); p.BrowserPath != deep || p.Selection.Directory != deep {
		t.Fatal("picker failed to restore remembered directory", p)
	}
	if p := decodePicker(map[string]any{"path": "."}); p.BrowserPath != "." || p.Selection.Directory != deep {
		t.Fatal("browsing root changed the remembered workspace", p)
	}
	if r := call("pc_open_workspace_picker", map[string]any{"path": "../outside"}); !r.IsError {
		t.Fatal("picker escaped the allowed root")
	}
	decodeInfo := func(r *mcp.CallToolResult) workspace.Info {
		t.Helper()
		b, _ := json.Marshal(r.StructuredContent)
		var info workspace.Info
		if e := json.Unmarshal(b, &info); e != nil {
			t.Fatal(e)
		}
		return info
	}
	chatA := decodeInfo(callChat("chat-a", "pc_get_workspace", map[string]any{}))
	if chatA.SessionBound {
		t.Fatal("new chat must not inherit global workspace as an accepted binding", chatA)
	}
	boundResult := callChat("chat-a", "pc_open_workspace_picker", map[string]any{})
	if boundResult.IsError {
		t.Fatal(boundResult.Content)
	}
	bb, _ := json.Marshal(boundResult.StructuredContent)
	var boundPicker Picker
	if e := json.Unmarshal(bb, &boundPicker); e != nil {
		t.Fatal(e)
	}
	if !boundPicker.Selection.SessionBound || boundPicker.Selection.Directory != deep {
		t.Fatal("picker did not bind default workspace to chat-a", boundPicker.Selection)
	}
	chatB := decodeInfo(callChat("chat-b", "pc_get_workspace", map[string]any{}))
	if chatB.SessionBound {
		t.Fatal("chat-b inherited chat-a binding", chatB)
	}
	if r := callChat("chat-a", "pc_write_file", map[string]any{"directory": "AI", "branch": "", "path": "wrong.txt", "text": "x", "expected_revision": "new"}); !r.IsError {
		t.Fatal("chat-scoped target guard accepted a different directory")
	}
	for _, uri := range []string{ui.PickerURI, ui.PreviousPickerURI} {
		resource, e := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if e != nil {
			t.Fatal("directory picker resource unavailable", uri, e)
		}
		if len(resource.Contents) != 1 || resource.Contents[0].URI != uri || resource.Contents[0].MIMEType != ui.MIMEType || resource.Contents[0].Text != ui.Picker || !strings.Contains(resource.Contents[0].Text, `id="breadcrumbs"`) {
			t.Fatal("cached or current picker URI did not return the latest interactive UI", uri)
		}
	}
	for _, uri := range []string{ui.ApprovalURI, ui.PreviousApprovalURI} {
		resource, e := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if e != nil {
			t.Fatal("approval resource unavailable", uri, e)
		}
		if len(resource.Contents) != 1 || resource.Contents[0].URI != uri || resource.Contents[0].MIMEType != ui.MIMEType || resource.Contents[0].Text != ui.Approval || !strings.Contains(resource.Contents[0].Text, "Ожидайте. Команда выполняется") || !strings.Contains(resource.Contents[0].Text, "records_cursor") || strings.Contains(resource.Contents[0].Text, "PC.tool('pc_job_output'") || strings.Contains(resource.Contents[0].Text, "/*BRIDGE*/") {
			t.Fatal("cached or current approval URI did not return the latest UI", uri)
		}
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
	r = call("pc_start_job", map[string]any{"directory": ".", "branch": "", "args": []string{"echo", "run"}, "purpose": "run", "network": true, "seconds": 1})
	if !r.IsError {
		t.Fatal("run bypassed confirmation card")
	}
	r = call("pc_request_run", map[string]any{"directory": ".", "branch": "", "args": []string{"echo", "run"}, "purpose": "run", "network": true, "seconds": 1})
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

func TestDebugContextProbeOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		d := t.TempDir()
		root, state := filepath.Join(d, "root"), filepath.Join(d, "state")
		os.Mkdir(root, 0700)
		os.Mkdir(state, 0700)
		engine := &sandbox.Engine{MaxSeconds: 10}
		ws, e := workspace.New(root, state, engine)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		jm := jobs.New(ctx, ws, engine, 10)
		st, ct := mcp.NewInMemoryTransports()
		go New(ws, jm, Options{DebugContext: enabled}).Run(ctx, st)
		session, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if e != nil {
			t.Fatal(e)
		}
		list, e := session.ListTools(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, tool := range list.Tools {
			if tool.Name == DebugContextTool {
				found = true
				if v, _ := json.Marshal(tool.Meta["ui"]); string(v) != `{"visibility":["app"]}` {
					t.Fatal("diagnostic tool must be hidden from the model", string(v))
				}
			}
		}
		res, e := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: ui.PickerURI})
		if e != nil {
			t.Fatal(e)
		}
		flagged := strings.Contains(res.Contents[0].Text, "window.PC_DEBUG_CONTEXT=true")
		if found != enabled || flagged != enabled {
			t.Fatal("diagnostic probe must follow the operator setting", enabled, found, flagged)
		}
		session.Close()
		cancel()
		jm.Close()
		ws.Close()
	}
}
