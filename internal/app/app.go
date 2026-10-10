// Package app composes services and owns HTTP lifecycle and proxy boundaries.
package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/auth"
	"github.com/onnov/mcp/internal/config"
	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/preferences"
	"github.com/onnov/mcp/internal/tools"
	"github.com/onnov/mcp/internal/workspace"
)

func Handler(c config.Config) (http.Handler, error) {
	g := github.New(c.AllowDefaultBranchWrites)
	prefs, err := preferences.Open(c.StateDir)
	if err != nil {
		return nil, err
	}
	w := &workspace.Service{GitHub: g, Preferences: prefs}
	s := tools.New(tools.Services{GitHub: g, Workspace: w, OAuth: c.OAuthEnabled()})
	mux := http.NewServeMux()
	var transport http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	if c.OAuthEnabled() {
		a := auth.New(c, g)
		a.RegisterRoutes(mux)
		transport = a.Protect(transport)
	}
	mux.Handle("/mcp", transport)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{"localhost:" + port: true, "127.0.0.1:" + port: true, "[::1]:" + port: true}
	if c.PublicHost != "" {
		hosts[strings.ToLower(c.PublicHost)] = true
	}
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil {
			return nil, e
		}
		hosts[strings.ToLower(u.Host)] = true
	}
	return protectHTTP(mux, hosts), nil
}

func protectHTTP(next http.Handler, hosts map[string]bool) http.Handler {
	slots := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "unrecognized Host; configure MCP_PUBLIC_URL or -public-host", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !hosts[strings.ToLower(u.Host)] {
				http.Error(w, "origin not allowed", 403)
				return
			}
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "server busy", 429)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Run(args []string) error {
	c, err := config.Load(args)
	if err != nil {
		return err
	}
	runtime.GOMAXPROCS(c.GOMAXPROCS)
	handler, err := Handler(c)
	if err != nil {
		return err
	}
	if os.Getenv("MCP_WRITE_REPOS") != "" {
		log.Print("MCP_WRITE_REPOS is obsolete and ignored; GitHub user permissions now control repository access")
	}
	s := &http.Server{Addr: c.Addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 95 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := "anonymous, public read-only"
	if c.OAuthEnabled() {
		mode = "GitHub OAuth, user repositories and development tools"
	}
	log.Printf("GitHub MCP listening on http://%s/mcp (%s); GOMAXPROCS=%d", c.Addr, mode, c.GOMAXPROCS)
	if c.OAuthEnabled() {
		log.Printf("GitHub MCP: public endpoint %s/mcp; OAuth client_id=%s; callbacks: %s", c.PublicURL, c.MCPClientID, strings.Join(append([]string{c.RedirectURI}, auth.ClaudeRedirectURIs...), ", "))
	}
	return serve(ctx, s, s.ListenAndServe)
}

// ListenAndServe returns as soon as Shutdown closes the listener. Wait for
// Shutdown itself too, otherwise main would exit before active requests finish.
func serve(ctx context.Context, s *http.Server, listen func() error) error {
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		defer close(shutdownDone)
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := s.Shutdown(shutdown); e != nil {
			log.Printf("shutdown: %v", e)
			_ = s.Close()
		}
	}()
	if err := listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
}
