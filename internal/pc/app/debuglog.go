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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/onnov/mcp/internal/pc/ownerauth"
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
		return l.tag(x)
	case json.Number:
		return l.tag(x.String())
	default: // bool, null
		return x
	}
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
		default:
			tags := make([]any, len(values))
			for i, v := range values {
				tags[i] = l.tag(v)
			}
			value = tags
		}
		headers[name] = value
	}
	entry := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "client": ownerauth.ClientFromContext(r.Context()), "http_method": r.Method, "headers": headers}
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
