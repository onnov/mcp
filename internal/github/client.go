// Package github implements bounded, non-retrying calls to the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/onnov/mcp/internal/identity"
)

const maxAPIBytes = 8 << 20

type Client struct {
	HTTP                     *http.Client
	AllowDefaultBranchWrites bool
}
type Object map[string]any

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub HTTP %d: %s", e.Status, e.Message) }
func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

func New(allowDefault bool) *Client {
	return &Client{AllowDefaultBranchWrites: allowDefault, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (g *Client) Request(ctx context.Context, method, path string, query url.Values, body, out any) (http.Header, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("invalid internal API path")
	}
	u := url.URL{Scheme: "https", Host: "api.github.com", Path: path, RawQuery: query.Encode()}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	r.Header.Set("User-Agent", "ghf-mcp/3.0.0")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if p := identity.From(ctx); p != nil {
		if !time.Now().Before(p.Expires) {
			return nil, errors.New("GitHub authorization expired; reconnect")
		}
		r.Header.Set("Authorization", "Bearer "+p.GitHubToken)
	}
	resp, err := g.HTTP.Do(r)
	if err != nil {
		return nil, fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAPIBytes {
		return nil, errors.New("GitHub response exceeds 8 MiB; request a smaller page or nonrecursive tree")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &detail)
		message := Clip(detail.Message, 500)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == 401 {
			message += "; reconnect GitHub OAuth"
		}
		if resp.StatusCode == 409 {
			message += "; fetch the current branch/file SHA before retrying"
		}
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			message += "; remaining=" + resp.Header.Get("X-RateLimit-Remaining") + ", reset=" + resp.Header.Get("X-RateLimit-Reset")
		}
		return resp.Header, &APIError{Status: resp.StatusCode, Message: message}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("invalid GitHub JSON: %w", err)
		}
	}
	return resp.Header, nil
}

func (g *Client) Do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	_, err := g.Request(ctx, method, path, q, body, out)
	return err
}

func (g *Client) Get(ctx context.Context, path, ref string, out any) error {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	return g.Do(ctx, http.MethodGet, path, q, nil, out)
}

type Page struct {
	Items      []Object `json:"items"`
	NextPage   int      `json:"next_page"`
	TotalCount int      `json:"total_count,omitempty"`
	Incomplete bool     `json:"incomplete,omitempty"`
}

func PageQuery(page, limit int) (url.Values, error) {
	if page == 0 {
		page = 1
	}
	if limit == 0 {
		limit = 50
	}
	if page < 1 || page > 10000 || limit < 1 || limit > 100 {
		return nil, errors.New("page must be 1..10000 and limit 1..100")
	}
	return url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(limit)}}, nil
}

func (g *Client) List(ctx context.Context, path string, q url.Values, envelope string) (Page, error) {
	out := Page{Items: []Object{}, NextPage: -1}
	var raw json.RawMessage
	headers, err := g.Request(ctx, "GET", path, q, nil, &raw)
	if err != nil {
		return out, err
	}
	if envelope == "" {
		err = json.Unmarshal(raw, &out.Items)
	} else {
		var obj map[string]json.RawMessage
		err = json.Unmarshal(raw, &obj)
		if err == nil {
			err = json.Unmarshal(obj[envelope], &out.Items)
			_ = json.Unmarshal(obj["total_count"], &out.TotalCount)
			_ = json.Unmarshal(obj["incomplete_results"], &out.Incomplete)
		}
	}
	if err != nil {
		return out, err
	}
	if out.Items == nil {
		out.Items = []Object{}
	}
	if HasNext(headers) {
		n, _ := strconv.Atoi(q.Get("page"))
		out.NextPage = n + 1
	}
	return out, nil
}

func HasNext(h http.Header) bool { return strings.Contains(h.Get("Link"), `rel="next"`) }
