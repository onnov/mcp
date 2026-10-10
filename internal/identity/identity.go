// Package identity carries the authenticated GitHub identity across HTTP and MCP.
package identity

import (
	"context"
	"errors"
	"time"
)

type Principal struct {
	GitHubToken string `json:"-"`
	UserID      int64
	Login       string
	Expires     time.Time
	// Client is the chat client that obtained this grant: chatgpt or claude.
	Client string
}

type contextKey struct{}

func With(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(contextKey{}).(*Principal)
	return p
}

func Require(ctx context.Context) (*Principal, error) {
	p := From(ctx)
	if p == nil || p.GitHubToken == "" || p.UserID <= 0 || !time.Now().Before(p.Expires) {
		return nil, errors.New("GitHub OAuth login required; reconnect the plugin")
	}
	return p, nil
}
