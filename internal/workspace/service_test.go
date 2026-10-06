package workspace

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
	"github.com/onnov/mcp/internal/preferences"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSelectionRestoresBranchHistoryAndReadonlyRecovery(t *testing.T) {
	prefs, e := preferences.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := github.New(false)
	g.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"id":10,"name":"repo","owner":{"login":"owner"},"default_branch":"master"}`
		code := 200
		if strings.Contains(r.URL.Path, "/branches/") {
			body = `{"name":"main"}`
			if strings.HasSuffix(r.URL.Path, "/deleted") {
				code = 404
				body = `{"message":"not found"}`
			}
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	s := &Service{GitHub: g, Preferences: prefs}
	ctx := identity.With(context.Background(), &identity.Principal{UserID: 42, GitHubToken: "secret", Expires: time.Now().Add(time.Hour)})
	first, e := s.Select(ctx, "owner", "repo", "")
	if e != nil || first.Selection.Branch != "main" {
		t.Fatalf("main preference: %+v %v", first, e)
	}
	if _, e := s.Select(ctx, "owner", "repo", "feature"); e != nil {
		t.Fatal(e)
	}
	if e := prefs.Set(42, preferences.Selection{Owner: "other", Repo: "repo", Branch: "main", RepositoryID: 11}); e != nil {
		t.Fatal(e)
	}
	back, e := s.Select(ctx, "owner", "repo", "")
	if e != nil || back.Selection.Branch != "feature" {
		t.Fatalf("per-repository history: %+v %v", back, e)
	}
	v := back.Selection
	v.Branch = "deleted"
	if e := prefs.Set(42, v); e != nil {
		t.Fatal(e)
	}
	recovered, e := s.Selection(ctx)
	if e != nil || recovered.Selection.Branch != "main" {
		t.Fatalf("fallback: %+v %v", recovered, e)
	}
	if saved, _ := prefs.Get(42); saved.Branch != "deleted" {
		t.Fatal("read-only restore changed preference")
	}
	if _, ok := prefs.Get(43); ok {
		t.Fatal("choice leaked to another user")
	}
}
