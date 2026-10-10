package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/workspace"
)

func requestSession(req *mcp.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	raw, ok := req.Params.Meta["openai/session"]
	if !ok {
		return ""
	}
	session, ok := raw.(string)
	if !ok || session == "" || len(session) > 512 || strings.IndexByte(session, 0) >= 0 {
		return ""
	}
	return session
}

func sessionTarget(ctx context.Context, req *mcp.CallToolRequest, ws *workspace.Service) (string, workspace.Target, error) {
	session := requestSession(req)
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
