package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/workspace"
)

// Clients without openai/session (Claude) get a server-issued chat key that
// keeps each conversation's workspace binding separate.
func TestChatKeyScopesClientsWithoutSessionMetadata(t *testing.T) {
	d := t.TempDir()
	root, state := filepath.Join(d, "root"), filepath.Join(d, "state")
	os.MkdirAll(filepath.Join(root, "proj"), 0700)
	os.Mkdir(state, 0700)
	engine := &sandbox.Engine{MaxSeconds: 10}
	ws, err := workspace.New(root, state, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx, ws, engine, 10)
	defer jm.Close()
	st, ct := mcp.NewInMemoryTransports()
	go New(ws, jm).Run(ctx, st)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "claude-ai", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	type info struct {
		Chat         string `json:"chat"`
		Directory    string `json:"directory"`
		SessionBound bool   `json:"session_bound"`
		Selection    struct {
			Chat         string `json:"chat"`
			SessionBound bool   `json:"session_bound"`
		} `json:"selection"`
	}
	call := func(meta mcp.Meta, name string, args map[string]any) (info, *mcp.CallToolResult) {
		t.Helper()
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: name, Arguments: args})
		if err != nil || r.IsError {
			t.Fatal(name, err, r)
		}
		var out info
		b, _ := json.Marshal(r.StructuredContent)
		_ = json.Unmarshal(b, &out)
		return out, r
	}

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		var parsed struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(schema, &parsed)
		if _, ok := parsed.Properties["chat"]; !ok {
			t.Fatal("tool does not accept chat", tool.Name, string(schema))
		}
		if tool.Name == "pc_start_job" {
			for _, field := range []string{"directory", "branch", "args", "purpose"} {
				if _, ok := parsed.Properties[field]; !ok {
					t.Fatal("run input lost a request field", field, string(schema))
				}
			}
		}
	}

	first, r := call(nil, "pc_get_workspace", map[string]any{})
	if !chatKeyPattern.MatchString(first.Chat) || r.Meta[chatMetaKey] != first.Chat || first.SessionBound {
		t.Fatal("a new chat must get a key and no binding", first, r.Meta)
	}
	k1 := first.Chat
	picker, r := call(nil, "pc_open_workspace_picker", map[string]any{"chat": k1})
	if picker.Chat != k1 || !picker.Selection.SessionBound || r.Meta[chatMetaKey] != k1 {
		t.Fatal("opening the picker must bind the default to this chat", picker)
	}
	if again, _ := call(nil, "pc_get_workspace", map[string]any{"chat": k1}); !again.SessionBound || again.Directory != "." {
		t.Fatal("the chat key must restore this chat's binding", again)
	}

	second, _ := call(nil, "pc_get_workspace", map[string]any{})
	k2 := second.Chat
	if k2 == k1 || second.SessionBound {
		t.Fatal("another conversation must get its own unbound key", second)
	}
	if _, r := call(nil, "pc_select_workspace", map[string]any{"chat": k2, "directory": "proj", "branch": ""}); r.Meta[chatMetaKey] != k2 {
		t.Fatal("card and model calls must echo the chat key", r.Meta)
	}
	if one, _ := call(nil, "pc_get_workspace", map[string]any{"chat": k1}); one.Directory != "." {
		t.Fatal("selecting a project in one chat changed another chat", one)
	}
	if two, _ := call(nil, "pc_get_workspace", map[string]any{"chat": k2}); two.Directory != "proj" || !two.SessionBound {
		t.Fatal("second chat lost its own project", two)
	}
	call(nil, "pc_list_jobs", map[string]any{"chat": k2})

	if out, r := call(nil, "pc_get_workspace", map[string]any{"chat": "none"}); out.Chat != "" || r.Meta[chatMetaKey] != nil {
		t.Fatal("a card without a key must not mint a new chat", out, r.Meta)
	}
	if r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pc_get_workspace", Arguments: map[string]any{"chat": "forged"}}); err == nil && !r.IsError {
		t.Fatal("malformed chat key accepted")
	}
	if out, r := call(mcp.Meta{"openai/session": "chatgpt-1"}, "pc_get_workspace", map[string]any{}); out.Chat != "" || r.Meta[chatMetaKey] != nil {
		t.Fatal("ChatGPT sessions must not receive chat keys", out, r.Meta)
	}
}
