package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
