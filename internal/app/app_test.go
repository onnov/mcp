package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHostOriginAndHealthProxyBoundary(t *testing.T) {
	h := protectHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), map[string]bool{"127.0.0.1:8181": true, "mcp.example.com": true})
	for _, v := range []struct {
		host, origin string
		status       int
	}{{"127.0.0.1:8181", "", 204}, {"mcp.example.com", "", 204}, {"mcp.example.com", "https://mcp.example.com", 204}, {"attacker.example", "", 403}, {"mcp.example.com", "https://attacker.example", 403}, {"mcp.example.com", "null", 403}} {
		r := httptest.NewRequest("GET", "http://"+v.host+"/healthz", nil)
		r.Header.Set("Origin", v.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != v.status {
			t.Fatalf("host=%s origin=%s: %d", v.host, v.origin, w.Code)
		}
	}
}

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { close(entered); <-release; w.WriteHeader(204) })}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, s, func() error { return s.Serve(listener) }) }()
	clientDone := make(chan error, 1)
	go func() {
		r, e := http.Get("http://" + listener.Addr().String())
		if e == nil {
			_ = r.Body.Close()
		}
		clientDone <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case e := <-finished:
		close(release)
		t.Fatalf("server returned before active request finished: %v", e)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case e := <-finished:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
	if e := <-clientDone; e != nil {
		t.Fatal(e)
	}
}
