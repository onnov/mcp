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

func TestRequestLogSplitsTracesAndMasksCardContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	l, err := newRequestLog(path)
	if err != nil {
		t.Fatal(err)
	}
	h := l.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pc_debug_client_context","arguments":{"context":{"referrer":"https://claude.ai/chat/7d0c2b1e-1111-4222-8333-444455556666?x=secret-q","initialize":{"hostContext":{"theme":"dark","toolInfo":{"id":"toolu_abcdef"}}}}}}}`
	for _, span := range []string{"1111111111111111", "2222222222222222"} {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		r.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-"+span+"-01")
		r.Header.Set("X-Cloud-Trace-Context", "105445aa7843bc8bf206b12000100000/"+span+";o=1")
		r.Header.Set("Baggage", "conversation=c-77,user=u-1")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, secret := range []string{"0af7651916cd43dd", "7d0c2b1e", "secret-q", "toolu_abcdef", "c-77", "105445aa"} {
		if strings.Contains(text, secret) {
			t.Fatal("log leaked", secret)
		}
	}
	type logged struct {
		Headers struct {
			Traceparent []struct {
				Trace map[string]any `json:"trace_id"`
				Span  map[string]any `json:"span_id"`
			} `json:"Traceparent"`
			Baggage []map[string]any `json:"Baggage"`
		} `json:"headers"`
		RPC []struct {
			Context map[string]any `json:"client_context"`
		} `json:"rpc"`
	}
	var entries []logged
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var e logged
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 2 {
		t.Fatal(text)
	}
	a, b := entries[0].Headers.Traceparent[0], entries[1].Headers.Traceparent[0]
	if a.Trace["tag"] != b.Trace["tag"] || a.Span["tag"] == b.Span["tag"] {
		t.Fatal("trace id must be comparable separately from span id", text)
	}
	if _, ok := entries[0].Headers.Baggage[0]["conversation"]; !ok {
		t.Fatal("baggage keys must stay readable", text)
	}
	referrer, _ := entries[0].RPC[0].Context["referrer"].(map[string]any)
	if referrer["url_host"] != "claude.ai" || len(referrer["path_segments"].([]any)) != 2 {
		t.Fatal("card referrer must keep host and tag path segments", text)
	}
}

func TestRequestLogKeepsCardDiagnosticsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	l, err := newRequestLog(path)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pc_debug_client_context","arguments":{"context":{"event":"apply","received":["ui/notifications/tool-input","ui/notifications/tool-result"],"key_source":"tool-input","has_key":true,"other":"c_0123456789abcdef0123456789abcdef"}}}}`
	l.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", strings.NewReader(body)))
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, want := range []string{`"event":"apply"`, `"ui/notifications/tool-input"`, `"key_source":"tool-input"`, `"has_key":true`} {
		if !strings.Contains(text, want) {
			t.Fatal("card diagnostics must stay readable", want, text)
		}
	}
	if strings.Contains(text, "0123456789abcdef") {
		t.Fatal("other card fields must stay masked", text)
	}
}
