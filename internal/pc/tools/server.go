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

const instructions = `Single-owner PC development plugin with RW execution in the selected Git checkout (or selected non-Git directory). Local commands can read/write/delete files, run tests/smoke/apps/scripts and local Git; purpose is descriptive metadata, never authorization. Use explicit directory and branch, inspect/select before editing, preserve dirty checkouts and read revisions before file writes. Commands from a subdirectory of a checkout see the entire checkout; inspect git_root and explain that boundary. Start user-authorized local commands with pc_start_job. Network/credentials or operator confirm-commands policy require pc_request_run's exact-command card; nonce is private app metadata. Do not ask again for an already authorized smoke/script run. Poll pc_job_status; retrieve middle logs with pc_job_output. Stop long-lived commands with pc_cancel_job or pc_cancel_all_jobs and poll until terminal. Jobs use all available CPU cores, isolated project caches and host memory/disk reserves. Network uses a public-egress proxy; custom clients must support HTTP(S)_PROXY. Private destinations require explicit operator policy. GitHub credentials stay in a host-side proxy, outside the job. No host home, SSH-agent, OAuth or tunnel credentials are mounted. Jobs on the same checkout may run concurrently and allow live file edits; branch transitions stay locked until every job exits. Concurrent command/IDE writes require care because revision checking of existing files is optimistic, not an atomic CAS. Report sandbox/resource setup failures without insecure fallbacks. Begin project selection with pc_open_workspace_picker; use path="." to browse the configured root or omit to restore selection. Show its interactive card instead of a prose project list.`

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
type OutputInput struct {
	ID    string `json:"id"`
	After int    `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type ApproveInput struct {
	ID            string `json:"id"`
	ApprovalNonce string `json:"approval_nonce"`
}

func baseDescriptor(name, description string, read bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Meta: mcp.Meta{"securitySchemes": []map[string]any{{"type": "noauth"}}, "ui": map[string]any{"visibility": []string{"model", "app"}}}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: boolptr(name == "pc_write_file" || name == "pc_delete_file" || name == "pc_start_job" || name == "pc_approve_run" || name == "pc_cancel_job" || name == "pc_cancel_all_jobs"), OpenWorldHint: boolptr(false)}}
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
	s := mcp.NewServer(&mcp.Implementation{Name: "pc-mcp", Title: "PC development workspace", Version: "1.1.0"}, &mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Extensions: map[string]any{"io.modelcontextprotocol/ui": map[string]any{}}}})
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
	start := descriptor("pc_start_job", "Start asynchronous local development commands: build, test, smoke, app, scripts and local Git. Commands can read/write/delete project files; purpose is metadata. Network/credentials (or operator confirm-commands policy) use pc_request_run. Poll returned job ID; cancel when finished.", false)
	uiTool(start, ui.ApprovalURI)
	mcp.AddTool(s, start, func(ctx context.Context, _ *mcp.CallToolRequest, in jobs.Request) (*mcp.CallToolResult, jobs.View, error) {
		if jm.NeedsApproval(in) {
			return nil, jobs.View{}, errors.New("this command needs pc_request_run and user confirmation")
		}
		v, _, e := jm.Prepare(in)
		if e == nil {
			v, e = jm.Start(ctx, v.ID, "")
		}
		return nil, v, e
	})
	request := descriptor("pc_request_run", "Show the exact command and its capabilities for optional confirmation or required network/credential approval. Creates a pending job; never starts it. Use when the user authorized execution; confirmation belongs to the card.", false)
	uiTool(request, ui.ApprovalURI)
	request.Annotations.DestructiveHint = boolptr(false)
	request.Annotations.OpenWorldHint = boolptr(true)
	mcp.AddTool(s, request, func(_ context.Context, _ *mcp.CallToolRequest, in jobs.Request) (*mcp.CallToolResult, jobs.View, error) {
		v, nonce, e := jm.PrepareApproval(in)
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
	mcp.AddTool(s, descriptor("pc_cancel_all_jobs", "Stop all plugin commands and descendants; invalidate all pending approvals. Poll returned IDs until terminal. Does not kill unrelated host processes.", false), func(_ context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"jobs": jm.CancelAll()}, nil
	})
	mcp.AddTool(s, descriptor("pc_job_output", "Read retained job output by sequence cursor, including the middle of logs. Bounded retention; reports any eviction. Limit 1..200.", true), func(_ context.Context, _ *mcp.CallToolRequest, in OutputInput) (*mcp.CallToolResult, jobs.OutputPage, error) {
		v, e := jm.Output(in.ID, in.After, in.Limit)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_list_jobs", "List bounded job history without console logs; jobs survive chats, not a server restart.", true), func(_ context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"jobs": jm.List()}, nil
	})
	for _, r := range []struct{ uri, name, html string }{{ui.PickerURI, "workspace-picker", ui.Picker}, {ui.LegacyPickerURI, "workspace-picker-legacy", ui.Picker}, {ui.PreviousPickerURI, "workspace-picker-v2", ui.Picker}, {ui.ApprovalURI, "command-confirmation", ui.Approval}, {ui.LegacyApprovalURI, "command-confirmation-legacy", ui.Approval}} {
		meta := mcp.Meta{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}}}, "openai/ui": map[string]any{"availableDisplayModes": []string{"inline", "fullscreen"}}}
		if r.uri == ui.PickerURI || r.uri == ui.LegacyPickerURI || r.uri == ui.PreviousPickerURI {
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
