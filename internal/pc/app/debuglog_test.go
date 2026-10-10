package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestLogMasksIdentifiersAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug", "mcp-requests.jsonl")
	l, err := newRequestLog(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	h := l.wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
	}))
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"claude-ai","version":"0.1.0"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pc_read_file","arguments":{"path":"secret-plan.txt"},"_meta":{"chat":"conv_0123456789abcdef","progressToken":7}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pc_get_workspace","arguments":{},"_meta":{"chat":"conv_0123456789abcdef"}}}`,
	}
	for _, body := range bodies {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer very-secret-token")
		r.Header.Set("Mcp-Session-Id", "3f2c9a1e-0000-4000-8000-123456789abc")
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), r)
		if seen != body {
			t.Fatal("request body was not passed through")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"very-secret-token", "secret-plan.txt", "0123456789abcdef", "3f2c9a1e"} {
		if strings.Contains(text, secret) {
			t.Fatal("log leaked", secret)
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"claude-ai"`) || !strings.Contains(lines[1], `"argument_keys":["path"]`) || !strings.Contains(lines[1], `"prefixed:conv_"`) {
		t.Fatal(text)
	}
	tag := func(line string) string {
		var e struct {
			RPC []struct {
				Meta map[string]map[string]any `json:"meta"`
			} `json:"rpc"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil || len(e.RPC) != 1 {
			t.Fatal(err, line)
		}
		return e.RPC[0].Meta["chat"]["tag"].(string)
	}
	if tag(lines[1]) != tag(lines[2]) {
		t.Fatal("equal identifiers must get equal tags")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0600 {
		t.Fatal("log must be private", st.Mode())
	}
}
