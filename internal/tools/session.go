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
	"github.com/onnov/mcp/internal/workspace"
)

// ChatInput is accepted by every tool. Clients that do not identify chats
// (no _meta["openai/session"], e.g. Claude) receive a server-issued key from
// get_selection or open_repository_picker; passing it back scopes the
// repository and branch to that chat.
type ChatInput struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat key returned as chat by get_selection; pass it unchanged in every call of this chat. Not needed in ChatGPT"`
}

const chatMetaKey = "ghf_chat"

// noChat is sent by cards without a known key; it never issues a new one.
const noChat = "none"

var chatKeyPattern = regexp.MustCompile(`^c_[0-9a-f]{32}$`)

type chatContextKey struct{}
type sessionContextKey struct{}
type boundNowKey struct{}

func newChatKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "c_" + hex.EncodeToString(b[:])
}

func openaiSession(meta mcp.Meta) string {
	session, ok := meta["openai/session"].(string)
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

// sessionFromContext is "openai/session" (ChatGPT), "chat:<key>" (server-issued
// key) or empty when the call names no chat.
func sessionFromContext(ctx context.Context) string {
	session, _ := ctx.Value(sessionContextKey{}).(string)
	return session
}

// boundNow reports that this call bound its chat to the last choice.
func boundNow(ctx context.Context) bool {
	bound, _ := ctx.Value(boundNowKey{}).(bool)
	return bound
}

// issuesChat lists the entry points that start a chat's repository flow.
func issuesChat(tool string) bool {
	return tool == "get_selection" || tool == "open_repository_picker"
}

// chatMiddleware resolves the chat once per tool call: ChatGPT's session, or a
// server-issued key. The key is also returned in the result metadata, where
// cards read it for their own tool calls.
func chatMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		req, ok := request.(*mcp.CallToolRequest)
		if method != "tools/call" || !ok || req.Params == nil {
			return next(ctx, method, request)
		}
		if session := openaiSession(req.Params.Meta); session != "" {
			return next(context.WithValue(ctx, sessionContextKey{}, session), method, request)
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
			// A card that never learned its chat key: no chat, no new key.
		case args.Chat != "":
			return nil, errors.New("invalid chat key; call get_selection without chat to get a new one")
		case issuesChat(req.Params.Name):
			chat = newChatKey()
		}
		if chat == "" {
			return next(ctx, method, request)
		}
		ctx = context.WithValue(context.WithValue(ctx, chatContextKey{}, chat), sessionContextKey{}, "chat:"+chat)
		result, err := next(ctx, method, request)
		if r, ok := result.(*mcp.CallToolResult); ok && r != nil {
			if r.Meta == nil {
				r.Meta = mcp.Meta{}
			}
			r.Meta[chatMetaKey] = chat
		}
		return result, err
	}
}

// bindMiddleware binds an unbound chat to the user's last choice on its first
// tool call, whichever tool that is. select_repository binds its own explicit
// choice instead.
func bindMiddleware(ws *workspace.Service) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			req, ok := request.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || req.Params == nil || req.Params.Name == "select_repository" || ws == nil {
				return next(ctx, method, request)
			}
			if created, err := ws.BindDefault(ctx, sessionFromContext(ctx)); err == nil && created {
				ctx = context.WithValue(ctx, boundNowKey{}, true)
			}
			return next(ctx, method, request)
		}
	}
}
