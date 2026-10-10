package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/preferences"
)

func chatKey(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	key, _ := r.Meta[chatMetaKey].(string)
	var out struct {
		Chat string `json:"chat"`
	}
	raw, _ := json.Marshal(r.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	if key != out.Chat {
		t.Fatalf("_meta.%s=%q but chat=%q", chatMetaKey, key, out.Chat)
	}
	return key
}

func TestClaudeChatsGetOwnKeysAndBindings(t *testing.T) {
	d := chatFixtures(t)
	ctx, session := connect(t, d)
	if e := d.Workspace.Preferences.Set(42, preferences.Selection{Owner: "owner", Repo: "one", Branch: "main", RepositoryID: 1}); e != nil {
		t.Fatal(e)
	}
	ra, va := callIn(t, ctx, session, nil, "get_selection", map[string]any{})
	rb, vb := callIn(t, ctx, session, nil, "open_repository_picker", map[string]any{})
	a, b := chatKey(t, ra), chatKey(t, rb)
	if !chatKeyPattern.MatchString(a) || !chatKeyPattern.MatchString(b) || a == b {
		t.Fatalf("chat keys %q %q", a, b)
	}
	// The first call of a chat binds it to the last choice and says so.
	if va.Selection.Repo != "one" || vb.Selection.Repo != "one" {
		t.Fatalf("new chats not bound to the last choice: %+v %+v", va, vb)
	}
	var message struct {
		Message      string `json:"message"`
		SessionBound bool   `json:"session_bound"`
	}
	raw, _ := json.Marshal(ra.StructuredContent)
	_ = json.Unmarshal(raw, &message)
	if !message.SessionBound || !strings.Contains(message.Message, "just bound") || !strings.Contains(message.Message, "owner/one @ main") {
		t.Fatalf("binding not reported: %+v", message)
	}
	// A choice in chat A rebinds only chat A and becomes the last choice.
	callIn(t, ctx, session, nil, "select_repository", map[string]any{"chat": a, "owner": "owner", "repo": "two", "branch": "dev"})
	if _, v := callIn(t, ctx, session, nil, "get_selection", map[string]any{"chat": b}); v.Selection.Repo != "one" || v.Selection.Branch != "main" {
		t.Fatalf("chat B moved: %+v", v)
	}
	if _, v := callIn(t, ctx, session, nil, "get_selection", map[string]any{"chat": a}); v.Selection.Repo != "two" || v.Selection.Branch != "dev" {
		t.Fatalf("chat A: %+v", v)
	}
	// Read tools without owner/repo use the chat's own repository.
	repoOf := func(chat string) string {
		r, _ := callIn(t, ctx, session, nil, "get_repository", map[string]any{"chat": chat})
		var repo struct {
			Name string `json:"name"`
		}
		raw, _ := json.Marshal(r.StructuredContent)
		_ = json.Unmarshal(raw, &repo)
		return repo.Name
	}
	if repoOf(a) != "two" || repoOf(b) != "one" {
		t.Fatal("read tools ignore the chat binding")
	}
	// A new chat starts from the last choice, made in chat A.
	rc, vc := callIn(t, ctx, session, nil, "get_selection", map[string]any{})
	if c := chatKey(t, rc); c == a || c == b || vc.Selection.Repo != "two" || vc.Selection.Branch != "dev" {
		t.Fatalf("new chat %q: %+v", c, vc)
	}
	// The first call of a chat may be any tool; the chat is bound by it.
	callIn(t, ctx, session, mcp.Meta{"openai/session": "fresh"}, "list_branches", map[string]any{})
	if _, ok := d.Workspace.Preferences.Chat(42, preferences.SessionKey("fresh")); !ok {
		t.Fatal("first call of a ChatGPT chat did not bind it")
	}
}

func TestChatGPTSessionNoneAndInvalidKeys(t *testing.T) {
	d := chatFixtures(t)
	ctx, session := connect(t, d)
	if e := d.Workspace.Preferences.Set(42, preferences.Selection{Owner: "owner", Repo: "one", Branch: "main", RepositoryID: 1}); e != nil {
		t.Fatal(e)
	}
	r, v := callIn(t, ctx, session, mcp.Meta{"openai/session": "gpt"}, "get_selection", map[string]any{})
	if chatKey(t, r) != "" || v.Selection.Repo != "one" {
		t.Fatalf("ChatGPT got a chat key or no binding: %+v %+v", r.Meta, v)
	}
	if _, ok := d.Workspace.Preferences.Chat(42, preferences.SessionKey("gpt")); !ok {
		t.Fatal("ChatGPT chat not bound by openai/session")
	}
	r, v = callIn(t, ctx, session, nil, "get_selection", map[string]any{"chat": "none"})
	if chatKey(t, r) != "" || v.Selection.Repo != "one" {
		t.Fatalf(`chat "none" issued a key: %+v`, r.Meta)
	}
	r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_selection", Arguments: map[string]any{"chat": "c_short"}})
	if e == nil && !r.IsError {
		t.Fatal("malformed chat key accepted")
	}
	// Other tools never issue keys.
	r, _ = callIn(t, ctx, session, nil, "list_repositories", map[string]any{})
	if r.Meta[chatMetaKey] != nil {
		t.Fatal("list_repositories issued a chat key")
	}
}

func TestEveryToolAcceptsChat(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		d := fixtures(t, oauth)
		ctx, session := connect(t, d)
		listed, e := session.ListTools(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		for _, tool := range listed.Tools {
			raw, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			_ = json.Unmarshal(raw, &schema)
			if _, ok := schema.Properties["chat"]; !ok {
				t.Errorf("%s (OAuth=%t) has no chat parameter: %s", tool.Name, oauth, raw)
			}
		}
	}
}
