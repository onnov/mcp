package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/preferences"
	"github.com/onnov/mcp/internal/workspace"
)

var repoIDs = map[string]int{"one": 1, "two": 2, "three": 3}

// chatFixtures serves distinct repositories (one, two, three) where every
// branch exists, so chats can be told apart by repository and branch.
func chatFixtures(t *testing.T) Services {
	t.Helper()
	prefs, e := preferences.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := github.New(false)
	repo := func(name string) string {
		raw, _ := json.Marshal(map[string]any{"id": repoIDs[name], "name": name, "full_name": "owner/" + name, "owner": map[string]string{"login": "owner"}, "default_branch": "main", "permissions": map[string]bool{"push": true}})
		return string(raw)
	}
	g.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		body := `{}`
		switch {
		case r.URL.Path == "/user/repos":
			body = "[" + repo("one") + "," + repo("two") + "," + repo("three") + "]"
		case len(parts) == 3 && parts[0] == "repos":
			body = repo(parts[2])
		case len(parts) >= 5 && parts[3] == "branches":
			raw, _ := json.Marshal(map[string]any{"name": strings.Join(parts[4:], "/"), "commit": map[string]string{"sha": strings.Repeat("1", 40)}})
			body = string(raw)
		case len(parts) == 4 && parts[3] == "branches":
			body = `[{"name":"main"},{"name":"dev"}]`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return Services{GitHub: g, Workspace: &workspace.Service{GitHub: g, Preferences: prefs}, OAuth: true}
}

type selectionView struct {
	Selection struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	} `json:"selection"`
	Available bool `json:"available"`
}

func callIn(t *testing.T, ctx context.Context, session *mcp.ClientSession, meta mcp.Meta, name string, args map[string]any) (*mcp.CallToolResult, selectionView) {
	t.Helper()
	r, e := session.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: name, Arguments: args})
	if e != nil || r.IsError {
		t.Fatalf("%s: %v %+v", name, e, r)
	}
	var v selectionView
	raw, _ := json.Marshal(r.StructuredContent)
	_ = json.Unmarshal(raw, &v)
	return r, v
}

// Regression: the selection used to be one per user, so choosing a repository
// in one chat moved every other chat. Only APIs that existed before chat
// bindings are used, so this test also runs against the old code.
func TestChoiceInOneChatGPTChatDoesNotMoveAnother(t *testing.T) {
	d := chatFixtures(t)
	ctx, session := connect(t, d)
	if e := d.Workspace.Preferences.Set(42, preferences.Selection{Owner: "owner", Repo: "one", Branch: "main", RepositoryID: 1}); e != nil {
		t.Fatal(e)
	}
	first, second := mcp.Meta{"openai/session": "chat-1"}, mcp.Meta{"openai/session": "chat-2"}
	callIn(t, ctx, session, first, "get_selection", map[string]any{})
	callIn(t, ctx, session, second, "get_selection", map[string]any{})
	callIn(t, ctx, session, first, "select_repository", map[string]any{"owner": "owner", "repo": "two", "branch": "dev"})
	if _, v := callIn(t, ctx, session, second, "get_selection", map[string]any{}); v.Selection.Repo != "one" || v.Selection.Branch != "main" {
		t.Fatalf("other chat moved to %s @ %s", v.Selection.Repo, v.Selection.Branch)
	}
	if _, v := callIn(t, ctx, session, first, "get_selection", map[string]any{}); v.Selection.Repo != "two" || v.Selection.Branch != "dev" {
		t.Fatalf("chosen chat shows %s @ %s", v.Selection.Repo, v.Selection.Branch)
	}
}
