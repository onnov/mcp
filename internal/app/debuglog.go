package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onnov/mcp/internal/identity"
	"github.com/onnov/mcp/internal/tools"
)

// requestLog records the shape of authenticated MCP requests so the owner can
// see which identifiers a chat client sends (for example a chat or session ID)
// and share the file. Possible identifiers are replaced by keyed tags: equal
// values get equal tags within one server run, values are not recoverable.
// Tool arguments and file contents are never written; only argument names.
type requestLog struct {
	mu    sync.Mutex
	path  string
	key   []byte
	limit int64
}

const requestLogLimit = 10 << 20

// Header values that are protocol facts, not identifiers.
var clearHeaders = map[string]bool{"Accept": true, "Content-Type": true, "Mcp-Protocol-Version": true, "User-Agent": true, "Accept-Encoding": true, "Content-Length": true}
var secretHeaders = map[string]bool{"Authorization": true, "Cookie": true, "Proxy-Authorization": true}

var (
	uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexShape  = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	numShape  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	b64Shape  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	prefShape = regexp.MustCompile(`^([a-z]+[_-])[A-Za-z0-9]+$`)
)

func newRequestLog(path string) (*requestLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &requestLog{path: path, key: key, limit: requestLogLimit}, nil
}

// tag describes a possibly identifying value without revealing it.
func (l *requestLog) tag(value string) map[string]any {
	mac := hmac.New(sha256.New, l.key)
	mac.Write([]byte(value))
	shape := "text"
	switch {
	case value == "":
		shape = "empty"
	case uuidShape.MatchString(value):
		shape = "uuid"
	case numShape.MatchString(value):
		shape = "number"
	case hexShape.MatchString(value):
		shape = "hex"
	case prefShape.MatchString(value):
		// Keep a short type prefix such as "conv_" or "chat-": it names the ID kind.
		shape = "prefixed:" + prefShape.FindStringSubmatch(value)[1]
	case b64Shape.MatchString(value):
		shape = "token"
	}
	return map[string]any{"tag": hex.EncodeToString(mac.Sum(nil))[:12], "len": len(value), "shape": shape}
}

// mask keeps the JSON structure and key names, tagging every string and number.
func (l *requestLog) mask(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			if text, ok := item.(string); ok && strings.EqualFold(k, "traceparent") {
				out[k] = l.traceparent(text)
				continue
			}
			out[k] = l.mask(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = l.mask(item)
		}
		return out
	case string:
		if u, err := url.Parse(x); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return l.url(u)
		}
		return l.tag(x)
	case json.Number:
		return l.tag(x.String())
	default: // bool, null
		return x
	}
}

// url keeps the host (a site name, such as claude.ai) and tags each path
// segment and query value: a chat ID inside a URL then shows up as its own tag.
func (l *requestLog) url(u *url.URL) map[string]any {
	segments := []any{}
	for _, part := range strings.Split(strings.Trim(u.EscapedPath(), "/"), "/") {
		if part != "" {
			segments = append(segments, l.tag(part))
		}
	}
	query := map[string]any{}
	for k, values := range u.Query() {
		tags := make([]any, len(values))
		for i, v := range values {
			tags[i] = l.tag(v)
		}
		query[k] = tags
	}
	out := map[string]any{"url_scheme": u.Scheme, "url_host": u.Host, "path_segments": segments, "query": query}
	if u.Fragment != "" {
		out["fragment"] = l.tag(u.Fragment)
	}
	return out
}

// traceparent splits W3C version-traceid-spanid-flags so a trace ID shared
// by several requests is visible even when each request has its own span.
func (l *requestLog) traceparent(v string) any {
	parts := strings.Split(v, "-")
	if len(parts) != 4 {
		return l.tag(v)
	}
	return map[string]any{"version": parts[0], "trace_id": l.tag(parts[1]), "span_id": l.tag(parts[2]), "flags": parts[3]}
}

// cloudTrace splits Google's TRACE_ID/SPAN_ID;o=OPTIONS.
func (l *requestLog) cloudTrace(v string) any {
	trace, rest, ok := strings.Cut(v, "/")
	if !ok {
		return l.tag(v)
	}
	span, options, _ := strings.Cut(rest, ";")
	return map[string]any{"trace_id": l.tag(trace), "span_id": l.tag(span), "options": options}
}

// keyValues keeps the keys of a tracestate/baggage list and tags their values.
func (l *requestLog) keyValues(v string) any {
	out := map[string]any{}
	for _, item := range strings.Split(v, ",") {
		k, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			return l.tag(v)
		}
		out[k] = l.tag(value)
	}
	return out
}

// Card diagnostic fields are protocol facts (message names, where a key came
// from, booleans), never identifiers or user data: they stay readable.
var clearCardFields = map[string]bool{"event": true, "received": true, "key_source": true, "has_key": true, "has_result": true}

func (l *requestLog) cardContext(v any) any {
	fields, ok := v.(map[string]any)
	if !ok {
		return l.mask(v)
	}
	out := map[string]any{}
	for k, item := range fields {
		if clearCardFields[k] {
			out[k] = item
		} else {
			out[k] = l.mask(item)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (l *requestLog) rpcEntry(raw map[string]any) map[string]any {
	method, _ := raw["method"].(string)
	entry := map[string]any{"method": method}
	if _, ok := raw["id"]; ok {
		entry["id"] = l.mask(raw["id"])
	}
	params, _ := raw["params"].(map[string]any)
	if params == nil {
		return entry
	}
	entry["param_keys"] = sortedKeys(params)
	if meta, ok := params["_meta"]; ok {
		entry["meta"] = l.mask(meta)
	}
	switch method {
	case "initialize":
		// Client name, version, protocol and capabilities identify software, not a chat.
		for _, k := range []string{"clientInfo", "protocolVersion", "capabilities"} {
			if v, ok := params[k]; ok {
				entry[k] = v
			}
		}
	case "tools/call":
		entry["tool"], _ = params["name"].(string)
		if args, ok := params["arguments"].(map[string]any); ok {
			entry["argument_keys"] = sortedKeys(args)
			if entry["tool"] == tools.DebugContextTool {
				entry["client_context"] = l.cardContext(args["context"])
			}
		}
	case "resources/read":
		entry["uri"], _ = params["uri"].(string)
	}
	return entry
}

func (l *requestLog) record(r *http.Request, body []byte) {
	headers := map[string]any{}
	for name, values := range r.Header {
		var value any
		switch {
		case secretHeaders[name]:
			value = "[redacted]"
		case clearHeaders[name]:
			value = values
		case name == "Traceparent" || name == "X-Cloud-Trace-Context" || name == "Tracestate" || name == "Baggage":
			parsed := make([]any, len(values))
			for i, v := range values {
				switch name {
				case "Traceparent":
					parsed[i] = l.traceparent(v)
				case "X-Cloud-Trace-Context":
					parsed[i] = l.cloudTrace(v)
				default:
					parsed[i] = l.keyValues(v)
				}
			}
			value = parsed
		default:
			tags := make([]any, len(values))
			for i, v := range values {
				tags[i] = l.tag(v)
			}
			value = tags
		}
		headers[name] = value
	}
	client := ""
	if p := identity.From(r.Context()); p != nil {
		client = p.Client // chatgpt or claude; never the user's identity
	}
	entry := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "client": client, "http_method": r.Method, "headers": headers}
	if len(body) > 0 {
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		var payload any
		if d.Decode(&payload) != nil {
			entry["body"] = "unparsed (" + strconv.Itoa(len(body)) + " bytes)"
		} else {
			messages := []any{payload}
			if batch, ok := payload.([]any); ok {
				messages = batch
			}
			rpc := []any{}
			for _, m := range messages {
				if obj, ok := m.(map[string]any); ok {
					rpc = append(rpc, l.rpcEntry(obj))
				}
			}
			entry["rpc"] = rpc
		}
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := os.Stat(l.path); err == nil && st.Size()+int64(len(line)) > l.limit {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func (l *requestLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		l.record(r, body)
		next.ServeHTTP(w, r)
	})
}
