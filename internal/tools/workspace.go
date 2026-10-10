package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
	"github.com/onnov/mcp/internal/ui"
	"github.com/onnov/mcp/internal/workspace"
)

type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type MentionQuery struct {
	ChatInput
	Query string `json:"query"`
}
type Mention struct {
	Type        string `json:"type"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType"`
}
type Mentions struct {
	Items []Mention `json:"items"`
}

func registerWorkspace(s *mcp.Server, d Services) {
	add(s, d, "get_selection", "Get this chat's repository and branch, checking current access. The first call of a chat binds it to the user's last repository and branch and says so in message; report it to the user. Without ChatGPT session metadata the result includes chat: a key to pass as chat in every later call of this chat. Read-only: never changes GitHub.", true, false, func(ctx context.Context, _ Empty) (workspace.SelectionResult, error) {
		out, err := d.Workspace.SessionSelection(ctx, sessionFromContext(ctx))
		out.Chat = chatFromContext(ctx)
		if err == nil && boundNow(ctx) && out.Available {
			v := out.Selection
			out.Message = strings.TrimSpace("This chat was just bound to the user's last repository " + v.Owner + "/" + v.Repo + " @ " + v.Branch + ". Tell the user this is the active repository and branch; they can change it in the picker card (open_repository_picker). " + out.Message)
		}
		return out, err
	})
	add(s, d, "select_repository", "Bind a repository and branch to this chat and make it the default for new chats; other chats keep theirs. Does not change GitHub files. When branch is omitted reuse its remembered branch, otherwise initially main if present.", false, false, func(ctx context.Context, in SelectionInput) (workspace.SelectionResult, error) {
		out, err := d.Workspace.Select(ctx, sessionFromContext(ctx), in.Owner, in.Repo, in.Branch)
		out.Chat = chatFromContext(ctx)
		return out, err
	})
	add(s, d, "list_repositories", "List ALL repositories accessible to the authenticated user: owned, collaborating and organization repositories, public and private. Search scans up to five API pages. Follow next_page even if this page has no matches.", true, false, func(ctx context.Context, in QueryInput) (github.RepositoryPage, error) {
		return d.GitHub.Repositories(ctx, in.Query, in.Page, in.Limit)
	})
	for _, kind := range []string{"code", "issues"} {
		add(s, d, "search_"+kind, "Search "+kind+" within one repository. GitHub search limits and indexing apply. Repository/org/user qualifiers are supplied by the server; follow next_page and incomplete_results.", true, false, func(ctx context.Context, in SearchInput) (github.Page, error) {
			o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
			if e != nil {
				return github.Page{}, e
			}
			return d.GitHub.Search(ctx, o, r, in.Query, kind, in.Page, in.Limit)
		})
	}
	profile := descriptor("get_profile", "Return the connected GitHub account's stable ID and login. Never returns credentials.", true, false, true)
	profile.Meta["openai/profile"] = true
	mcp.AddTool(s, profile, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, Profile, error) {
		p, e := identity.Require(ctx)
		if e != nil {
			return nil, Profile{}, e
		}
		return nil, Profile{ID: strconv.FormatInt(p.UserID, 10), Name: p.Login}, nil
	})
	icon := mcp.Icon{Source: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.33"><circle cx="6" cy="4" r="2"/><circle cx="6" cy="16" r="2"/><circle cx="14" cy="7" r="2"/><path d="M6 6v8m8-5c0 4-8 1-8 5"/></svg>`)), MIMEType: "image/svg+xml", Sizes: []string{"20x20"}}
	picker := descriptor("open_repository_picker", "Open the searchable repository and branch picker for this chat. It shows this chat's repository and branch (a new chat starts from the user's last choice). Use this when the user asks to choose or switch a repository or branch. Without ChatGPT session metadata the result includes chat: pass it in every later call of this chat.", true, false, true)
	picker.Title = "Репозиторий и ветка"
	picker.Icons = []mcp.Icon{icon}
	picker.Meta["ui"] = map[string]any{"resourceUri": ui.URI, "visibility": []string{"model", "app"}}
	picker.Meta["openai/outputTemplate"] = ui.URI
	picker.Meta["openai/widgetAccessible"] = true
	picker.Meta["openai/ui"] = map[string]any{"entrypoints": []map[string]string{{"type": "global"}, {"type": "thread"}}}
	mcp.AddTool(s, picker, func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, workspace.PickerResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out, e := d.Workspace.Picker(ctx, sessionFromContext(ctx))
		out.Chat = chatFromContext(ctx)
		return nil, out, e
	})
	cardHTML := ui.HTML
	if d.DebugContext {
		// The flag must exist before the card script runs.
		cardHTML = strings.Replace(cardHTML, "<script>", "<script>window.GHF_DEBUG_CONTEXT=true;", 1)
	}
	// Only the current card and the previous one are served; both are the current HTML.
	for _, r := range []struct{ uri, name string }{{ui.URI, "repository-picker"}, {ui.PreviousURI, "repository-picker-previous"}} {
		s.AddResource(&mcp.Resource{URI: r.uri, Name: r.name, Title: "Репозиторий и ветка", MIMEType: ui.MIMEType, Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}}}, "openai/ui": map[string]any{"availableDisplayModes": []string{"inline", "fullscreen"}}}}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if _, e := identity.Require(ctx); e != nil {
				return nil, e
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.uri, MIMEType: ui.MIMEType, Text: cardHTML}}}, nil
		})
	}
	if d.DebugContext {
		probe := descriptor(DebugContextTool, "Diagnostics only: the picker card reports its host context to the server request log.", true, false, true)
		probe.Meta["ui"] = map[string]any{"visibility": []string{"app"}}
		mcp.AddTool(s, probe, func(context.Context, *mcp.CallToolRequest, struct {
			ChatInput
			Context map[string]any `json:"context"`
		}) (*mcp.CallToolResult, Empty, error) {
			return nil, Empty{}, nil
		})
	}
	mentions := descriptor("search_repository_mentions", "Search repository mentions for the desktop composer. Returns resource links; use the full picker for paginated search and branch selection.", true, false, true)
	mentions.Meta["openai/extensions"] = map[string]any{"mentions/search": map[string]any{}}
	mcp.AddTool(s, mentions, func(ctx context.Context, _ *mcp.CallToolRequest, in MentionQuery) (*mcp.CallToolResult, Mentions, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		out := Mentions{Items: []Mention{}}
		selection, e := d.Workspace.SessionSelection(ctx, sessionFromContext(ctx))
		if e != nil {
			return nil, out, e
		}
		page, e := d.GitHub.Repositories(ctx, in.Query, 1, 50)
		if e != nil {
			return nil, out, e
		}
		seen := map[string]bool{}
		appendRepo := func(owner, repo, branch, description string) {
			key := owner + "/" + repo
			if seen[key] {
				return
			}
			seen[key] = true
			u := url.URL{Scheme: "ghf", Host: "repository", Path: "/" + owner + "/" + repo}
			if branch != "" {
				u.RawQuery = url.Values{"branch": {branch}}.Encode()
			}
			out.Items = append(out.Items, Mention{Type: "resource_link", URI: u.String(), Name: key, Description: description, MIMEType: "application/json"})
		}
		if in.Query == "" && selection.Available {
			v := selection.Selection
			appendRepo(v.Owner, v.Repo, v.Branch, "Последний выбор · "+v.Branch)
		}
		for _, r := range page.Repositories {
			owner := r.Owner.Login
			if owner == "" {
				owner = strings.Split(r.FullName, "/")[0]
			}
			// Omit branch for other repositories so Select restores their own
			// history, then main/default, exactly like the full picker.
			branch := ""
			if selection.Available && r.ID == selection.Selection.RepositoryID {
				branch = selection.Selection.Branch
			}
			appendRepo(owner, r.Name, branch, r.Description)
		}
		return nil, out, nil
	})
	// Resolving a user-selected composer mention saves only the preference: this
	// chat's binding when ChatGPT names the chat, otherwise the last choice.
	// It never commits to GitHub. URI validation and live access checks still apply.
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "ghf://repository/{owner}/{repo}{?branch}", Name: "repository-selection", Title: "GitHub repository selection", MIMEType: "application/json", Description: "Resolve a chosen repository mention and remember that repository/branch for the authenticated user."}, func(ctx context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		u, e := url.Parse(r.Params.URI)
		if e != nil || u.Scheme != "ghf" || u.Host != "repository" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("invalid repository mention URI")
		}
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) != 2 {
			return nil, errors.New("invalid repository mention path")
		}
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil {
			return nil, e
		}
		for k, v := range q {
			if k != "branch" || len(v) != 1 {
				return nil, errors.New("invalid mention query")
			}
		}
		selection, e := d.Workspace.Select(ctx, openaiSession(r.Params.Meta), parts[0], parts[1], q.Get("branch"))
		if e != nil {
			return nil, e
		}
		body, e := json.Marshal(selection)
		if e != nil {
			return nil, e
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "application/json", Text: string(body)}}}, nil
	})
}
