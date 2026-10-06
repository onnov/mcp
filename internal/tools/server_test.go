package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
	"github.com/onnov/mcp/internal/preferences"
	"github.com/onnov/mcp/internal/ui"
	"github.com/onnov/mcp/internal/workspace"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtures(t *testing.T, oauth bool) Services {
	t.Helper()
	prefs, e := preferences.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := github.New(false)
	g.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"id":10,"name":"repo","full_name":"owner/repo","owner":{"login":"owner"},"default_branch":"main","permissions":{"push":true}}`
		if strings.Contains(r.URL.Path, "/branches/") {
			body = `{"name":"main","commit":{"sha":"1111111111111111111111111111111111111111"}}`
		}
		if strings.HasSuffix(r.URL.Path, "/branches") {
			body = `[{"name":"main","protected":false}]`
		}
		if r.URL.Path == "/user/repos" {
			body = "[" + body + "]"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return Services{GitHub: g, Workspace: &workspace.Service{GitHub: g, Preferences: prefs}, OAuth: oauth}
}

func connect(t *testing.T, d Services) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = identity.With(ctx, &identity.Principal{UserID: 42, Login: "owner", GitHubToken: "secret", Expires: time.Now().Add(time.Hour)})
	st, ct := mcp.NewInMemoryTransports()
	serverSession, e := New(d).Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = session.Close() })
	return ctx, session
}

func TestToolSchemasAndAnonymousSurface(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		d := fixtures(t, oauth)
		ctx, session := connect(t, d)
		listed, e := session.ListTools(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("OAuth=%t: %d tools", oauth, len(listed.Tools))
		names := map[string]*mcp.Tool{}
		for _, tool := range listed.Tools {
			names[tool.Name] = tool
		}
		if len(names) < 25 {
			t.Fatalf("too few tools: %d", len(names))
		}
		_, hasWrite := names["commit_files"]
		if hasWrite != oauth {
			t.Fatal("write surface not gated by OAuth")
		}
		if oauth {
			for _, name := range []string{"write_file", "commit_files", "fork_repository"} {
				raw, _ := json.Marshal(names[name].InputSchema)
				var schema struct {
					Required []string `json:"required"`
				}
				_ = json.Unmarshal(raw, &schema)
				for _, field := range []string{"owner", "repo"} {
					found := false
					for _, required := range schema.Required {
						if required == field {
							found = true
						}
					}
					if !found {
						t.Fatalf("%s does not require %s: %s", name, field, raw)
					}
				}
			}
			picker := names["open_repository_picker"]
			if picker.Meta["openai/outputTemplate"] != ui.URI {
				t.Fatal("picker resource not attached")
			}
		}
	}
}

func TestPickerMentionsAndSelectionThroughMCP(t *testing.T) {
	d := fixtures(t, true)
	ctx, session := connect(t, d)
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || r.IsError {
			t.Fatalf("%s: %v %+v", name, e, r)
		}
		return r
	}
	call("select_repository", map[string]any{"owner": "owner", "repo": "repo"})
	if v, ok := d.Workspace.Preferences.Get(42); !ok || v.Branch != "main" {
		t.Fatal("selection not saved by authenticated user ID")
	}
	call("open_repository_picker", map[string]any{})
	resource, e := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: ui.URI})
	if e != nil || len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "repo-search") {
		t.Fatalf("picker resource: %v %+v", e, resource)
	}
	mentions := call("search_repository_mentions", map[string]any{"query": ""})
	data, _ := json.Marshal(mentions.StructuredContent)
	var out Mentions
	if e := json.Unmarshal(data, &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Items) != 1 || out.Items[0].Type != "resource_link" {
		t.Fatalf("wrong mentions: %s", data)
	}
	if _, e := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: out.Items[0].URI}); e != nil {
		t.Fatal(e)
	}
}

func TestStatelessTransportPreservesPerRequestIdentity(t *testing.T) {
	d := fixtures(t, true)
	s := New(d)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	for _, id := range []int64{1, 2} {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_profile","arguments":{}}}`
		r := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-03-26")
		ctx := identity.With(r.Context(), &identity.Principal{UserID: id, Login: "test", GitHubToken: "secret", Expires: time.Now().Add(time.Hour)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(ctx))
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var result struct {
			Result struct {
				StructuredContent Profile `json:"structuredContent"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		expected := "1"
		if id == 2 {
			expected = "2"
		}
		if result.Error != nil || result.Result.StructuredContent.ID != expected {
			t.Fatalf("principal lost or crossed: %s", w.Body.String())
		}
	}
}
