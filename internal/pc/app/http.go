package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/config"
	"github.com/onnov/mcp/internal/pc/ownerauth"
	"github.com/onnov/mcp/internal/pc/sshtunnel"
)

func httpHandler(cfg config.Config, server *mcp.Server) (http.Handler, error) {
	auth, err := ownerauth.New(cfg.OAuth)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux)
	// Our outer Host/Origin allowlist handles DNS rebinding and the configured
	// public domain. The SDK's loopback-only default would reject that domain
	// after the reverse proxy forwards it to this loopback listener.
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	mcpSlots := make(chan struct{}, 8)
	mux.Handle("/mcp", auth.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case mcpSlots <- struct{}{}:
			defer func() { <-mcpSlots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "server busy", 429)
			return
		}
		transport.ServeHTTP(w, r)
	})))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	public, _ := url.Parse(cfg.OAuth.PublicURL)
	hosts := map[string]bool{strings.ToLower(public.Host): true, cfg.HTTPAddr: true}
	if cfg.SSH.RemoteAddr != "" {
		hosts[cfg.SSH.RemoteAddr] = true
	}
	publicSlots := make(chan struct{}, 4)
	publicLimit := &ownerauth.Limiter{Max: 120, Period: time.Minute}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "host not allowed", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != cfg.OAuth.PublicURL {
			http.Error(w, "origin not allowed", 403)
			return
		}
		if r.URL.Path != "/mcp" {
			if !publicLimit.Allow(ownerauth.ClientIP(r, cfg.OAuth.TrustedProxies)) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limited", 429)
				return
			}
			select {
			case publicSlots <- struct{}{}:
				defer func() { <-publicSlots }()
			default:
				w.Header().Set("Retry-After", "1")
				http.Error(w, "server busy", 429)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

func serveHTTP(ctx context.Context, cfg config.Config, mcpServer *mcp.Server) error {
	handler, err := httpHandler(cfg, mcpServer)
	if err != nil {
		return err
	}
	var tunnel *sshtunnel.Tunnel
	if cfg.Transport == "ssh" {
		tunnel, err = sshtunnel.New(cfg.SSH, cfg.SOCKS5Proxy)
		if err != nil {
			return err
		}
		if cfg.SOCKS5Proxy != nil {
			fmt.Fprintln(os.Stderr, "pc-mcp: SOCKS5 enabled for outgoing SSH; destination DNS is resolved by the proxy")
		}
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 95 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpDone := make(chan error, 1)
	go func() {
		httpDone <- server.Serve(&limitedListener{Listener: listener, slots: make(chan struct{}, 128)})
	}()
	var sshDone chan error
	if tunnel != nil {
		sshDone = make(chan error, 1)
		go func() { sshDone <- tunnel.Run(runCtx, listener.Addr().String()) }()
	}
	fmt.Fprintf(os.Stderr, "pc-mcp: authenticated HTTP listening on %s; public endpoint %s/mcp\n", listener.Addr(), cfg.OAuth.PublicURL)
	var result error
	httpStopped, sshStopped := false, false
	select {
	case <-ctx.Done():
	case result = <-httpDone:
		httpStopped = true
	case result = <-sshDone:
		sshStopped = true
		if result == nil {
			result = errors.New("SSH forwarding unexpectedly stopped")
		}
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
	}
	stop()
	if !httpStopped {
		<-httpDone
	}
	if sshDone != nil && !sshStopped {
		<-sshDone
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
