// Package app owns the PC server lifecycle and its outbound-only tunnel.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/config"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/netproxy"
	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/tools"
	"github.com/onnov/mcp/internal/pc/workspace"
	tunnelclient "github.com/openai/tunnel-client"
)

func Run(args []string) error {
	cfg, e := config.Parse(args)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return Serve(ctx, cfg)
}
func Serve(ctx context.Context, cfg config.Config) error {
	instance, e := lockState(cfg.State)
	if e != nil {
		return e
	}
	defer instance.Close()
	if cfg.Transport == "tunnel" {
		restore, err := netproxy.Install(cfg.SOCKS5Proxy)
		if err != nil {
			return err
		}
		defer restore()
		if cfg.SOCKS5Proxy != nil {
			fmt.Fprintln(os.Stderr, "pc-mcp: SOCKS5 proxy enabled; tunnel DNS is resolved by the proxy")
		}
	}
	engine := &sandbox.Engine{Cache: cfg.State + "/cache", Toolchains: cfg.Toolchains, AllowNetwork: cfg.Network, MaxSeconds: cfg.MaxSeconds, GHtoken: os.Getenv("PC_MCP_GH_TOKEN")}
	engine.Configure(cfg.Root)
	ws, e := workspace.New(cfg.Root, cfg.State, engine)
	if e != nil {
		return e
	}
	defer ws.Close()
	jm := jobs.New(ctx, ws, engine, cfg.MaxSeconds)
	defer jm.Close()
	server := tools.New(ws, jm)
	if cfg.Transport == "stdio" {
		err := server.Run(ctx, &mcp.StdioTransport{})
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Run(serverCtx, serverTransport) }()
	client, e := tunnelclient.New(tunnelclient.Config{TunnelID: cfg.TunnelID, APIKey: cfg.APIKey, OrganizationID: cfg.Organization, MaxInFlightRequests: 4}, clientTransport)
	if e != nil {
		return e
	}
	if e = client.Start(ctx); e != nil {
		return e
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(shutdown)
	}()
	fmt.Fprintln(os.Stderr, "pc-mcp: starting outbound OpenAI tunnel; MCP does not open an inbound port")
	go func() {
		if e := client.WaitUntilReady(serverCtx); e == nil {
			fmt.Fprintln(os.Stderr, "pc-mcp: tunnel connected")
		}
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-client.Done():
		return errors.New("tunnel runtime stopped")
	case e := <-done:
		if errors.Is(e, context.Canceled) {
			return nil
		}
		return e
	}
}
