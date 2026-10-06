// Package tools maps the MCP protocol to the PC services.
package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/files"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/ui"
	"github.com/onnov/mcp/internal/pc/workspace"
)

const instructions = `Local PC development server. Begin with pc_open_workspace_picker. When the user asks to see available directories, browse folders, display the directory tree, or choose/change a project, open the interactive workspace picker instead of replacing it with a prose directory list. Use path="." for available directories from the configured root; omit path to restore the remembered directory. Its browser can navigate any nesting depth; the text tree's depth limit does not limit browsing. pc_list_directory and pc_directory_tree also render this picker in UI-capable clients. Keep narration short when the card is shown. If no directory has been remembered, choose the project directory before editing. Use explicit directory and branch from this chat in every file operation/job; a saved selection is only a default for new chats. Inspect the directory, select or create a branch BEFORE editing. Never change a dirty checkout silently. Read revisions before writes. Only the configured root is accessible. Commands run in Linux bubblewrap, with one active job; edits and branch changes wait for it. Use pc_start_job for build/test/inspect. Poll pc_job_status with returned output.cursor, allowing the job to finish; never resend the whole output on every poll. Stdout and stderr are continuously drained, but only the first 100 records (32 KiB) and final 10 records (8 KiB) are retained. Stop/cancel long-lived apps when finished. After implementation and tests, ASK the user whether to run smoke tests or the application; use pc_request_run to show the actual command for confirmation. Never disguise a smoke/run/git/gh command as a test. Git/gh and any network/credential use require the confirmation card. Never request approval_nonce from the user; it is private app metadata. No host shell, SSH agent, home, OAuth token or tunnel credentials are available to ordinary jobs. Report failed sandbox setup instead of suggesting an insecure fallback. Git tools are optional; non-Git directories support normal file work.`

type Empty struct{}
type PickerInput struct {
	Path string `json:"path,omitempty" jsonschema:"Starting directory relative to the allowed root; dot shows all top-level directories. Omit to restore the remembered directory."`
}
type Picker struct {
	Selection    workspace.Info `json:"selection"`
	Directories  files.Page     `json:"directories"`
	Capabilities map[string]any `json:"capabilities"`
	BrowserPath  string         `json:"browser_path"`
}
type Path struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Query  string `json:"query,omitempty"`
}
type TreeInput struct {
	Path  string `json:"path"`
	Depth int    `json:"depth,omitempty"`
}
type Directory struct {
	Directory string `json:"directory"`
}
type Select struct {
	workspace.Target
	Create     bool   `json:"create,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}
type FileInput struct {
	workspace.Target
	Path string `json:"path"`
}
type WriteInput struct {
	FileInput
	Text             string `json:"text"`
	ExpectedRevision string `json:"expected_revision"`
}
type DeleteInput struct {
	FileInput
	ExpectedRevision string `json:"expected_revision"`
}
type JobInput struct {
	ID    string `json:"id"`
	After int    `json:"after,omitempty"`
}
type ApproveInput struct {
	ID            string `json:"id"`
	ApprovalNonce string `json:"approval_nonce"`
}

func baseDescriptor(name, description string, read bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Meta: mcp.Meta{"securitySchemes": []map[string]any{{"type": "noauth"}}, "ui": map[string]any{"visibility": []string{"model", "app"}}}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: boolptr(!read), OpenWorldHint: boolptr(false)}}
}
func boolptr(b bool) *bool { return &b }
func uiTool(t *mcp.Tool, uri string) {
	t.Meta["ui"] = map[string]any{"resourceUri": uri, "visibility": []string{"model", "app"}}
	t.Meta["openai/outputTemplate"] = uri
	t.Meta["openai/widgetAccessible"] = true
}

type Options struct{ OAuth bool }

func New(ws *workspace.Service, jm *jobs.Manager, options ...Options) *mcp.Server {
	oauth := len(options) > 0 && options[0].OAuth
	descriptor := func(name, description string, read bool) *mcp.Tool {
		t := baseDescriptor(name, description, read)
		if oauth {
			t.Meta["securitySchemes"] = []map[string]any{{"type": "oauth2", "scopes": []string{"pc"}}}
		}
		return t
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "pc-mcp", Title: "PC development workspace", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Extensions: map[string]any{"io.modelcontextprotocol/ui": map[string]any{}}}})
	picker := descriptor("pc_open_workspace_picker", "Use this when the user asks to show available PC directories, browse folders, show the directory tree, or choose/change a workspace. Opens an interactive file-manager card with folder navigation at any depth, search, remembered selection and a branch selector. Set path to dot to browse from root, or omit it to restore the last directory. Let the user choose in the card; do not substitute a prose list.", true)
	uiTool(picker, ui.PickerURI)
	picker.Title = "PC: каталог и ветка"
	picker.Meta["openai/ui"] = map[string]any{"entrypoints": []map[string]string{{"type": "global"}, {"type": "thread"}}}
	mcp.AddTool(s, picker, func(ctx context.Context, _ *mcp.CallToolRequest, in PickerInput) (*mcp.CallToolResult, Picker, error) {
		selection, e := ws.Selection(ctx)
		if e != nil {
			return nil, Picker{}, e
		}
		path := in.Path
		if path == "" {
			path = "."
			if selection.Available {
				path = selection.Directory
			}
		}
		page, e := ws.List(path, 0, 100, "")
		return nil, Picker{Selection: selection, Directories: page, Capabilities: ws.Capabilities(), BrowserPath: path}, e
	})
	mcp.AddTool(s, descriptor("pc_capabilities", "Show detected tools, sandbox readiness and network policy.", true), func(_ context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, map[string]any, error) {
		return nil, ws.Capabilities(), nil
	})
	mcp.AddTool(s, descriptor("pc_get_workspace", "Get remembered directory and current actual branch. Does not switch branches.", true), func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, workspace.Info, error) {
		o, e := ws.Selection(ctx)
		return nil, o, e
	})
	listing := descriptor("pc_list_directory", "List directories/files relative to configured root with pagination and render an interactive folder/branch picker. For a user asking to browse available projects prefer pc_open_workspace_picker. Symlinks are identified and never followed by the picker.", true)
	uiTool(listing, ui.PickerURI)
	mcp.AddTool(s, listing, func(_ context.Context, _ *mcp.CallToolRequest, in Path) (*mcp.CallToolResult, files.Page, error) {
		o, e := ws.List(in.Path, in.Offset, in.Limit, in.Query)
		return &mcp.CallToolResult{Meta: mcp.Meta{"pc_browser_path": in.Path}}, o, e
	})
	tree := descriptor("pc_directory_tree", "Return a bounded directory tree and render an interactive folder/branch picker at the requested path. For visual project selection prefer pc_open_workspace_picker. Text tree defaults to depth 3, max 5, max 300 entries; the interactive browser has no nesting-depth limit. Generated and hidden directories omitted from text tree; use the picker to browse them.", true)
	uiTool(tree, ui.PickerURI)
	mcp.AddTool(s, tree, func(_ context.Context, _ *mcp.CallToolRequest, in TreeInput) (*mcp.CallToolResult, workspace.Tree, error) {
		o, e := ws.Tree(in.Path, in.Depth)
		return &mcp.CallToolResult{Meta: mcp.Meta{"pc_browser_path": in.Path}}, o, e
	})
	mcp.AddTool(s, descriptor("pc_inspect_workspace", "Inspect a candidate directory: current/local branches, dirty state and Git availability.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in Directory) (*mcp.CallToolResult, workspace.Info, error) {
		o, e := ws.Inspect(ctx, in.Directory)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_select_workspace", "Remember directory; switch/create a local branch before editing. Blank branch keeps current. Refuses switching dirty checkouts or while jobs run.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in Select) (*mcp.CallToolResult, workspace.Info, error) {
		o, e := ws.Select(ctx, in.Target, in.Create, in.BaseBranch)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_read_file", "Read a regular file <=512 KiB and its SHA256 revision, relative to explicit project directory.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in FileInput) (*mcp.CallToolResult, files.File, error) {
		o, e := ws.Read(ctx, in.Target, in.Path)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_write_file", "Write/create file atomically; expected_revision from read_file or new. Creates parent folders; checks actual branch and rejects stale revisions.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, files.File, error) {
		o, e := ws.Write(ctx, in.Target, in.Path, in.Text, in.ExpectedRevision)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_delete_file", "Delete a regular file after checking its exact revision. No recursive deletion.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, map[string]bool, error) {
		e := ws.Remove(ctx, in.Target, in.Path, in.ExpectedRevision)
		return nil, map[string]bool{"deleted": e == nil}, e
	})
	mcp.AddTool(s, descriptor("pc_start_job", "Start bounded asynchronous build/test/inspect with no network or credentials. Args are argv, not an implicit shell. Returns job ID; poll status for output.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in jobs.Request) (*mcp.CallToolResult, jobs.View, error) {
		if jobs.NeedsApproval(in) {
			return nil, jobs.View{}, errors.New("this command needs pc_request_run and user confirmation")
		}
		v, _, e := jm.Prepare(in)
		if e == nil {
			v, e = jm.Start(ctx, v.ID, "")
		}
		return nil, v, e
	})
	request := descriptor("pc_request_run", "Present an actual smoke/app/git/gh/network command for user confirmation. Creates pending job, NEVER starts it. Explain the command and ask the user first.", false)
	uiTool(request, ui.ApprovalURI)
	request.Annotations.DestructiveHint = boolptr(false)
	request.Annotations.OpenWorldHint = boolptr(true)
	mcp.AddTool(s, request, func(_ context.Context, _ *mcp.CallToolRequest, in jobs.Request) (*mcp.CallToolResult, jobs.View, error) {
		if !jobs.NeedsApproval(in) {
			return nil, jobs.View{}, errors.New("use start_job for an ordinary build/test/inspect")
		}
		v, nonce, e := jm.Prepare(in)
		return &mcp.CallToolResult{Meta: mcp.Meta{"approval_nonce": nonce}}, v, e
	})
	approve := descriptor("pc_approve_run", "User confirmation card only. Submit the private one-use nonce from UI metadata.", false)
	approve.Annotations.OpenWorldHint = boolptr(true)
	approve.Meta["ui"] = map[string]any{"visibility": []string{"app"}}
	approve.Meta["openai/widgetAccessible"] = true
	mcp.AddTool(s, approve, func(ctx context.Context, _ *mcp.CallToolRequest, in ApproveInput) (*mcp.CallToolResult, jobs.View, error) {
		v, e := jm.Start(ctx, in.ID, in.ApprovalNonce)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_job_status", "Poll job with after=previous output.cursor. During execution returns new head records; after completion includes final tail and omitted count. after=0 replays retained output.", true), func(_ context.Context, _ *mcp.CallToolRequest, in JobInput) (*mcp.CallToolResult, jobs.View, error) {
		v, e := jm.Get(in.ID, in.After)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_cancel_job", "Cancel pending/running job and kill the sandbox process group. Poll status until cancelled.", false), func(_ context.Context, _ *mcp.CallToolRequest, in JobInput) (*mcp.CallToolResult, jobs.View, error) {
		v, e := jm.Cancel(in.ID)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_list_jobs", "List bounded job history without console logs; jobs survive chats, not a server restart.", true), func(_ context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"jobs": jm.List()}, nil
	})
	for _, r := range []struct{ uri, name, html string }{{ui.PickerURI, "workspace-picker", ui.Picker}, {ui.LegacyPickerURI, "workspace-picker-legacy", ui.Picker}, {ui.ApprovalURI, "command-confirmation", ui.Approval}} {
		meta := mcp.Meta{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}}}, "openai/ui": map[string]any{"availableDisplayModes": []string{"inline", "fullscreen"}}}
		if r.uri == ui.PickerURI || r.uri == ui.LegacyPickerURI {
			meta["openai/widgetDescription"] = "Interactive PC workspace browser: open nested folders, use clickable ancestor breadcrumbs, filter names, select a directory and its current/existing/new Git branch. The last confirmed workspace is remembered. Let the user make their selection in this card."
		}
		s.AddResource(&mcp.Resource{URI: r.uri, Name: r.name, MIMEType: ui.MIMEType, Meta: meta}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.uri, MIMEType: ui.MIMEType, Text: r.html, Meta: meta}}}, nil
		})
	}
	// Useful plain-text fallback for clients with no MCP Apps support.
	s.AddResource(&mcp.Resource{URI: "pc://workflow", Name: "workflow", MIMEType: "text/plain"}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, _ := json.Marshal(map[string]string{"instructions": instructions})
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "pc://workflow", MIMEType: "text/plain", Text: string(b)}}}, nil
	})
	return s
}
