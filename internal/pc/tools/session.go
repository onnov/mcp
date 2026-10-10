package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/workspace"
)

// ChatInput is accepted by every tool. Clients that do not identify chats
// (no _meta["openai/session"], e.g. Claude) receive a server-issued key from
// pc_get_workspace or pc_open_workspace_picker; passing it back scopes the
// workspace binding and job history to that chat.
type ChatInput struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat key returned as chat by pc_get_workspace; pass it unchanged in every pc_* call of this chat. Not needed in ChatGPT"`
}

const chatMetaKey = "pc_chat"

// noChat is sent by cards without a known key; it never issues a new one.
const noChat = "none"

var chatKeyPattern = regexp.MustCompile(`^c_[0-9a-f]{32}$`)

type chatContextKey struct{}

func newChatKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "c_" + hex.EncodeToString(b[:])
}

func openaiSession(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	session, ok := req.Params.Meta["openai/session"].(string)
	if !ok || session == "" || len(session) > 512 || strings.IndexByte(session, 0) >= 0 {
		return ""
	}
	return session
}

// chatFromContext is the key this call is scoped to, issued or supplied.
func chatFromContext(ctx context.Context) string {
	chat, _ := ctx.Value(chatContextKey{}).(string)
	return chat
}

func requestSession(ctx context.Context, req *mcp.CallToolRequest) string {
	if session := openaiSession(req); session != "" {
		return session
	}
	if chat := chatFromContext(ctx); chat != "" {
		return "chat:" + chat
	}
	return ""
}

// issuesChat lists the entry points that start a chat's workspace flow.
func issuesChat(tool string) bool {
	return tool == "pc_get_workspace" || tool == "pc_open_workspace_picker"
}

// chatMiddleware resolves the chat key once per tool call and returns it in
// the result metadata, where cards read it for their own tool calls.
func chatMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		req, ok := request.(*mcp.CallToolRequest)
		if method != "tools/call" || !ok || req.Params == nil || openaiSession(req) != "" {
			return next(ctx, method, request)
		}
		var args struct {
			Chat string `json:"chat"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		chat := ""
		switch {
		case chatKeyPattern.MatchString(args.Chat):
			chat = args.Chat
		case args.Chat == noChat:
			// A card that never learned its chat key: shared session, no new key.
		case args.Chat != "":
			return nil, errors.New("invalid chat key; call pc_get_workspace without chat to get a new one")
		case issuesChat(req.Params.Name):
			chat = newChatKey()
		}
		if chat == "" {
			return next(ctx, method, request)
		}
		result, err := next(context.WithValue(ctx, chatContextKey{}, chat), method, request)
		if r, ok := result.(*mcp.CallToolResult); ok && r != nil {
			if r.Meta == nil {
				r.Meta = mcp.Meta{}
			}
			r.Meta[chatMetaKey] = chat
		}
		return result, err
	}
}

type boundNowKey struct{}

// boundNow reports that this call bound its chat to the default workspace.
func boundNow(ctx context.Context) bool {
	bound, _ := ctx.Value(boundNowKey{}).(bool)
	return bound
}

// bindMiddleware binds an unbound chat to the last used workspace on its
// first tool call, whichever tool that is. pc_select_workspace binds its own
// explicit choice instead.
func bindMiddleware(ws *workspace.Service) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			req, ok := request.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || req.Params == nil || req.Params.Name == "pc_select_workspace" {
				return next(ctx, method, request)
			}
			if session := requestSession(ctx, req); session != "" {
				if created, err := ws.BindDefault(ctx, session); err == nil && created {
					ctx = context.WithValue(ctx, boundNowKey{}, true)
				}
			}
			return next(ctx, method, request)
		}
	}
}

func sessionTarget(ctx context.Context, req *mcp.CallToolRequest, ws *workspace.Service) (string, workspace.Target, error) {
	session := requestSession(ctx, req)
	if session == "" {
		info, err := ws.Selection(ctx)
		return "", info.Target, err
	}
	info, err := ws.SessionSelection(ctx, session, false)
	if err != nil {
		return session, workspace.Target{}, err
	}
	if !info.SessionBound {
		return session, workspace.Target{}, errors.New("no workspace is bound to this chat; open pc_open_workspace_picker before project work")
	}
	return session, info.Target, nil
}
