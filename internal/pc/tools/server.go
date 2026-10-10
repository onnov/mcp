// Package tools maps the MCP protocol to the PC services.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/files"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/ui"
	"github.com/onnov/mcp/internal/pc/workspace"
)

const instructions = `Single-owner PC development plugin with RW execution in the selected Git checkout (or selected non-Git directory). Local commands can read/write/delete files, run tests/smoke/apps/scripts and local Git; purpose is descriptive metadata, never authorization. Workspace selection and job history are chat-scoped. ChatGPT identifies chats with _meta["openai/session"]. Other clients (e.g. Claude) get a chat key: the result of pc_get_workspace or pc_open_workspace_picker contains chat; pass it unchanged as the chat argument in every later pc_* call of this conversation, never reuse it in another conversation, and if it is lost call pc_get_workspace without chat to start a new binding. At the start of project work in a chat, call pc_get_workspace. The first pc_* call of a chat binds it to the last used workspace (directory and branch) automatically: tell the user which directory and branch are active and that they can change them. Open pc_open_workspace_picker when the user wants to see or change workspaces, or when session_bound is false because the last workspace is unavailable; a choice there or pc_select_workspace rebinds only this chat immediately and becomes the default for new chats. Use explicit directory and branch matching the chat binding, preserve dirty checkouts and read revisions before file writes. Commands from a subdirectory of a checkout see the entire checkout; inspect git_root and explain that boundary. Start user-authorized local commands with pc_start_job. Network/credentials or operator confirm-commands policy require pc_request_run's exact-command card; nonce is private app metadata. Do not ask again for an already authorized smoke/script run. Poll pc_job_status; its output.records stream is the primary bounded console contract. Drain all output.records pages through terminal state and include the command console output in the next chat response instead of leaving it only in the card; if retained output was evicted or model-context console was truncated, say so and fetch remaining retained pages when available. pc_job_output is optional for separate retained-log paging. pc_list_jobs is scoped to the current chat and merges live jobs with persisted history from the project's .pcctx directory, so command/output history survives server restarts. After completing a Git coding/editing task, call pc_git_change_summary with the explicit directory/branch and the task base (normally main), then report every created/modified/deleted file with additions/deletions and total +/-. Use + and - diff-style markers (or equivalent green/red UI where supported). Stop long-lived commands with pc_cancel_job or pc_cancel_all_jobs and poll until terminal. Jobs use all available CPU cores, isolated project caches and host memory/disk reserves. Network uses a public-egress proxy; custom clients must support HTTP(S)_PROXY. Private destinations require explicit operator policy. GitHub HTTPS tokens stay in a host-side proxy. Approved credential Git SSH jobs may receive the configured SSH private key and known_hosts as read-only mounts; SSH egress is restricted to github.com:22 and credential Git commands are allowlisted. No host home, SSH-agent, OAuth or tunnel-control credentials are mounted. Jobs on the same checkout may run concurrently and allow live file edits; branch transitions stay locked until every job exits. Concurrent command/IDE writes require care because revision checking of existing files is optimistic, not an atomic CAS. Report sandbox/resource setup failures without insecure fallbacks. Project context is stored under .pcctx in the Git root (or selected non-Git directory), automatically ignored via .gitignore and pruned when old job snapshots make it too large. Begin project selection with pc_get_workspace or pc_open_workspace_picker; use path="." to browse the configured root or omit to restore this chat's selection/default. Show the interactive card instead of a prose project list.`

type Empty struct{}
type PickerInput struct {
	ChatInput
	Path string `json:"path,omitempty" jsonschema:"Starting directory relative to the allowed root; dot shows all top-level directories. Omit to restore the remembered directory."`
}
type Picker struct {
	Chat         string         `json:"chat,omitempty"`
	Selection    workspace.Info `json:"selection"`
	Directories  files.Page     `json:"directories"`
	Capabilities map[string]any `json:"capabilities"`
	BrowserPath  string         `json:"browser_path"`
}
type Path struct {
	ChatInput
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Query  string `json:"query,omitempty"`
}
type TreeInput struct {
	ChatInput
	Path  string `json:"path"`
	Depth int    `json:"depth,omitempty"`
}
type Directory struct {
	ChatInput
	Directory string `json:"directory"`
}
type Select struct {
	ChatInput
	workspace.Target
	Create     bool   `json:"create,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}
type FileInput struct {
	ChatInput
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
type ChangeSummaryInput struct {
	ChatInput
	workspace.Target
	Base string `json:"base,omitempty" jsonschema:"Git base ref for the task summary; defaults to main"`
}
type JobInput struct {
	ChatInput
	ID    string `json:"id"`
	After int    `json:"after,omitempty"`
}
type OutputInput struct {
	ChatInput
	ID    string `json:"id"`
	After int    `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// RunInput is a command request scoped to a chat.
type RunInput struct {
	ChatInput
	jobs.Request
}
type ApproveInput struct {
	ChatInput
	ID            string `json:"id"`
	ApprovalNonce string `json:"approval_nonce"`
}

func baseDescriptor(name, description string, read bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Meta: mcp.Meta{"securitySchemes": []map[string]any{{"type": "noauth"}}, "ui": map[string]any{"visibility": []string{"model", "app"}}}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: boolptr(name == "pc_write_file" || name == "pc_delete_file" || name == "pc_start_job" || name == "pc_approve_run" || name == "pc_cancel_job" || name == "pc_cancel_all_jobs"), OpenWorldHint: boolptr(false)}}
}
func boolptr(b bool) *bool { return &b }
func branchLabel(branch string) string {
	if branch == "" {
		return "(no Git)"
	}
	return branch
}
func uiTool(t *mcp.Tool, uri string) {
	t.Meta["ui"] = map[string]any{"resourceUri": uri, "visibility": []string{"model", "app"}}
	t.Meta["openai/outputTemplate"] = uri
	t.Meta["openai/widgetAccessible"] = true
}

type Options struct {
	OAuth bool
	// DebugContext lets cards report their host context for request diagnostics.
	DebugContext bool
}

// DebugContextTool receives a card's host context; the HTTP request log masks it.
const DebugContextTool = "pc_debug_client_context"

func New(ws *workspace.Service, jm *jobs.Manager, options ...Options) *mcp.Server {
	oauth := len(options) > 0 && options[0].OAuth
	debugContext := len(options) > 0 && options[0].DebugContext
	descriptor := func(name, description string, read bool) *mcp.Tool {
		t := baseDescriptor(name, description, read)
		if oauth {
			t.Meta["securitySchemes"] = []map[string]any{{"type": "oauth2", "scopes": []string{"pc"}}}
		}
		return t
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "pc-mcp", Title: "PC development workspace", Version: "1.1.8"}, &mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Extensions: map[string]any{"io.modelcontextprotocol/ui": map[string]any{}}}})
	s.AddReceivingMiddleware(chatMiddleware, bindMiddleware(ws))
	picker := descriptor("pc_open_workspace_picker", "Use this when the user asks to show available PC directories, browse folders, show the directory tree, or choose/change a workspace. Opens an interactive file-manager card with folder navigation at any depth, search, remembered selection and a branch selector. Set path to dot to browse from root, or omit it to restore the last directory. Let the user choose in the card; do not substitute a prose list.", true)
	uiTool(picker, ui.PickerURI)
	picker.Title = "PC: каталог и ветка"
	picker.Meta["openai/ui"] = map[string]any{"entrypoints": []map[string]string{{"type": "global"}, {"type": "thread"}}}
	mcp.AddTool(s, picker, func(ctx context.Context, req *mcp.CallToolRequest, in PickerInput) (*mcp.CallToolResult, Picker, error) {
		selection, e := ws.SessionSelection(ctx, requestSession(ctx, req), true)
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
		return nil, Picker{Chat: chatFromContext(ctx), Selection: selection, Directories: page, Capabilities: ws.Capabilities(), BrowserPath: path}, e
	})
	mcp.AddTool(s, descriptor("pc_capabilities", "Show detected tools, sandbox readiness and network policy.", true), func(_ context.Context, _ *mcp.CallToolRequest, _ ChatInput) (*mcp.CallToolResult, map[string]any, error) {
		capabilities := ws.Capabilities()
		capabilities["approval_uri"] = ui.ApprovalURI
		capabilities["picker_uri"] = ui.PickerURI
		digest := sha256.Sum256([]byte(ui.Approval))
		capabilities["approval_html_sha256"] = hex.EncodeToString(digest[:])
		return nil, capabilities, nil
	})
	mcp.AddTool(s, descriptor("pc_get_workspace", "Get this chat's workspace binding: directory, branch and Git state. The first call of a chat binds it to the last used workspace and says so in message; report it to the user. If session_bound is false the last workspace is unavailable: open pc_open_workspace_picker. Without ChatGPT session metadata the result includes chat: a key to pass as chat in every later pc_* call of this chat. Does not switch branches.", true), func(ctx context.Context, req *mcp.CallToolRequest, _ ChatInput) (*mcp.CallToolResult, workspace.Info, error) {
		o, e := ws.SessionSelection(ctx, requestSession(ctx, req), false)
		o.Chat = chatFromContext(ctx)
		if e == nil && boundNow(ctx) {
			o.Message = strings.TrimSpace("This chat was just bound to the last used workspace " + o.Directory + " @ " + branchLabel(o.Branch) + ". Tell the user this is the active workspace; they can change it with pc_open_workspace_picker. " + o.Message)
		}
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
	mcp.AddTool(s, descriptor("pc_select_workspace", "Bind directory/branch to the current chat and update the global default for future chats; switch/create a local branch before editing. Blank branch keeps current. Refuses switching dirty checkouts or while jobs run.", false), func(ctx context.Context, req *mcp.CallToolRequest, in Select) (*mcp.CallToolResult, workspace.Info, error) {
		o, e := ws.SelectSession(ctx, requestSession(ctx, req), in.Target, in.Create, in.BaseBranch)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_read_file", "Read a regular file <=512 KiB and its SHA256 revision, relative to explicit project directory.", true), func(ctx context.Context, req *mcp.CallToolRequest, in FileInput) (*mcp.CallToolResult, files.File, error) {
		if e := ws.ValidateSessionTarget(requestSession(ctx, req), in.Target); e != nil {
			return nil, files.File{}, e
		}
		o, e := ws.Read(ctx, in.Target, in.Path)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_write_file", "Write/create file atomically; expected_revision from read_file or new. Creates parent folders; checks actual branch and rejects stale revisions.", false), func(ctx context.Context, req *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, files.File, error) {
		if e := ws.ValidateSessionTarget(requestSession(ctx, req), in.Target); e != nil {
			return nil, files.File{}, e
		}
		o, e := ws.Write(ctx, in.Target, in.Path, in.Text, in.ExpectedRevision)
		return nil, o, e
	})
	mcp.AddTool(s, descriptor("pc_delete_file", "Delete a regular file after checking its exact revision. No recursive deletion.", false), func(ctx context.Context, req *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, map[string]bool, error) {
		if e := ws.ValidateSessionTarget(requestSession(ctx, req), in.Target); e != nil {
			return nil, map[string]bool{"deleted": false}, e
		}
		e := ws.Remove(ctx, in.Target, in.Path, in.ExpectedRevision)
		return nil, map[string]bool{"deleted": e == nil}, e
	})
	mcp.AddTool(s, descriptor("pc_git_change_summary", "Read-only Git task summary from the merge-base with base (default main) through the current working tree. Includes committed branch work, staged/unstaged tracked changes, untracked created files, per-file status and line additions/deletions plus totals. Call after coding/editing work before the final user-facing summary.", true), func(ctx context.Context, req *mcp.CallToolRequest, in ChangeSummaryInput) (*mcp.CallToolResult, workspace.ChangeSummary, error) {
		if e := ws.ValidateSessionTarget(requestSession(ctx, req), in.Target); e != nil {
			return nil, workspace.ChangeSummary{}, e
		}
		o, e := ws.ChangeSummary(ctx, in.Target, in.Base)
		return nil, o, e
	})
	start := descriptor("pc_start_job", "Start asynchronous local development commands: build, test, smoke, app, scripts and local Git. Commands can read/write/delete project files; purpose is metadata. Network/credentials (or operator confirm-commands policy) use pc_request_run. Poll returned job ID; cancel when finished.", false)
	uiTool(start, ui.ApprovalURI)
	mcp.AddTool(s, start, func(ctx context.Context, req *mcp.CallToolRequest, run RunInput) (*mcp.CallToolResult, jobs.View, error) {
		in := run.Request
		if jm.NeedsApproval(in) {
			return nil, jobs.View{}, errors.New("this command needs pc_request_run and user confirmation")
		}
		session := requestSession(ctx, req)
		v, _, e := jm.PrepareSession(session, in)
		if e == nil {
			v, e = jm.StartSession(ctx, session, v.ID, "")
		}
		return nil, v, e
	})
	request := descriptor("pc_request_run", "Show the exact command and its capabilities for optional confirmation or required network/credential approval. Creates a pending job; never starts it. Use when the user authorized execution; confirmation belongs to the card.", false)
	uiTool(request, ui.ApprovalURI)
	request.Annotations.DestructiveHint = boolptr(false)
	request.Annotations.OpenWorldHint = boolptr(true)
	mcp.AddTool(s, request, func(ctx context.Context, req *mcp.CallToolRequest, run RunInput) (*mcp.CallToolResult, jobs.View, error) {
		v, nonce, e := jm.PrepareApprovalSession(requestSession(ctx, req), run.Request)
		return &mcp.CallToolResult{Meta: mcp.Meta{"approval_nonce": nonce}}, v, e
	})
	approve := descriptor("pc_approve_run", "User confirmation card only. Submit the private one-use nonce from UI metadata.", false)
	approve.Annotations.OpenWorldHint = boolptr(true)
	approve.Meta["ui"] = map[string]any{"visibility": []string{"app"}}
	approve.Meta["openai/widgetAccessible"] = true
	mcp.AddTool(s, approve, func(ctx context.Context, req *mcp.CallToolRequest, in ApproveInput) (*mcp.CallToolResult, jobs.View, error) {
		v, e := jm.StartSession(ctx, requestSession(ctx, req), in.ID, in.ApprovalNonce)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_job_status", "Self-contained current-chat job status and console-output contract. Poll with after=previous output.records_cursor. Can read persisted terminal jobs from .pcctx after a server restart.", true), func(ctx context.Context, req *mcp.CallToolRequest, in JobInput) (*mcp.CallToolResult, jobs.View, error) {
		session, target, e := sessionTarget(ctx, req, ws)
		if e != nil {
			return nil, jobs.View{}, e
		}
		v, e := jm.GetSession(session, target, in.ID, in.After)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_cancel_job", "Cancel a pending/running job belonging to the current chat and kill its sandbox process group. Poll status until cancelled.", false), func(ctx context.Context, req *mcp.CallToolRequest, in JobInput) (*mcp.CallToolResult, jobs.View, error) {
		v, e := jm.CancelSession(requestSession(ctx, req), in.ID)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_cancel_all_jobs", "Stop commands and pending approvals belonging to the current chat only; jobs of other chats keep running.", false), func(ctx context.Context, req *mcp.CallToolRequest, _ ChatInput) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"jobs": jm.CancelAllSession(requestSession(ctx, req))}, nil
	})
	mcp.AddTool(s, descriptor("pc_job_output", "Optional current-chat retained-output paging API, including persisted terminal logs from .pcctx. Limit 1..200.", true), func(ctx context.Context, req *mcp.CallToolRequest, in OutputInput) (*mcp.CallToolResult, jobs.OutputPage, error) {
		session, target, e := sessionTarget(ctx, req, ws)
		if e != nil {
			return nil, jobs.OutputPage{}, e
		}
		v, e := jm.OutputSession(session, target, in.ID, in.After, in.Limit)
		return nil, v, e
	})
	mcp.AddTool(s, descriptor("pc_list_jobs", "List bounded command history for the current chat, merging live jobs with persisted .pcctx terminal history across server restarts.", true), func(ctx context.Context, req *mcp.CallToolRequest, _ ChatInput) (*mcp.CallToolResult, map[string]any, error) {
		session, target, e := sessionTarget(ctx, req, ws)
		if e != nil {
			return nil, nil, e
		}
		rows, e := jm.ListSession(session, target)
		return nil, map[string]any{"jobs": rows}, e
	})
	if debugContext {
		probe := descriptor(DebugContextTool, "Diagnostics only: a card reports its host context to the server request log.", true)
		probe.Meta["ui"] = map[string]any{"visibility": []string{"app"}}
		mcp.AddTool(s, probe, func(context.Context, *mcp.CallToolRequest, struct {
			Context map[string]any `json:"context"`
		}) (*mcp.CallToolResult, Empty, error) {
			return nil, Empty{}, nil
		})
	}
	cardHTML := func(html string) string {
		if !debugContext {
			return html
		}
		// The first script is the bridge; the flag must exist before it runs.
		return strings.Replace(html, "<script>", "<script>window.PC_DEBUG_CONTEXT=true;", 1)
	}
	for _, r := range []struct{ uri, name, html string }{{ui.PickerURI, "workspace-picker", ui.Picker}, {ui.PreviousPickerURI, "workspace-picker-previous", ui.Picker}, {ui.ApprovalURI, "command-confirmation", ui.Approval}, {ui.PreviousApprovalURI, "command-confirmation-previous", ui.Approval}} {
		meta := mcp.Meta{"ui": map[string]any{"prefersBorder": true, "csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}}}, "openai/ui": map[string]any{"availableDisplayModes": []string{"inline", "fullscreen"}}}
		if r.html == ui.Picker {
			meta["openai/widgetDescription"] = "Interactive PC workspace browser: open nested folders, use clickable ancestor breadcrumbs, filter names, select a directory and its current/existing/new Git branch. The selection is bound to the current chat; a new chat is prefilled from the global last workspace and can accept it by simply closing the card. Let the user change it in this card when needed."
		}
		s.AddResource(&mcp.Resource{URI: r.uri, Name: r.name, MIMEType: ui.MIMEType, Meta: meta}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.uri, MIMEType: ui.MIMEType, Text: cardHTML(r.html), Meta: meta}}}, nil
		})
	}
	// Useful plain-text fallback for clients with no MCP Apps support.
	s.AddResource(&mcp.Resource{URI: "pc://workflow", Name: "workflow", MIMEType: "text/plain"}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		b, _ := json.Marshal(map[string]string{"instructions": instructions})
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "pc://workflow", MIMEType: "text/plain", Text: string(b)}}}, nil
	})
	return s
}
