package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onnov/mcp/internal/identity"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func authenticated(user int64) context.Context {
	return identity.With(context.Background(), &identity.Principal{UserID: user, Login: "test", GitHubToken: fmt.Sprintf("token-%d", user), Expires: time.Now().Add(time.Hour)})
}
func mockClient(fn transportFunc) *Client { g := New(false); g.HTTP.Transport = fn; return g }

func TestRepositoriesUseCurrentUserPrivateAccessAndContinueSearch(t *testing.T) {
	calls := 0
	g := mockClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.URL.Path != "/user/repos" || r.Header.Get("Authorization") != "Bearer token-7" {
			t.Fatalf("incorrect boundary: %v", r)
		}
		if r.URL.Query().Get("visibility") != "all" || !strings.Contains(r.URL.Query().Get("affiliation"), "organization_member") {
			t.Fatal("missing private/org visibility")
		}
		page := r.URL.Query().Get("page")
		res := response(200, `[{"id":1,"full_name":"org/unrelated"}]`)
		if page != "6" {
			res.Header.Set("Link", `<https://untrusted.example/user/repos>; rel="next"`)
		} else {
			res = response(200, `[{"id":6,"name":"wanted","full_name":"org/wanted","private":true}]`)
		}
		return res, nil
	})
	first, e := g.Repositories(authenticated(7), "wanted", 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(first.Repositories) != 0 || first.NextPage != 6 || calls != 5 {
		t.Fatalf("pagination lost: %+v, calls=%d", first, calls)
	}
	second, e := g.Repositories(authenticated(7), "wanted", first.NextPage, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(second.Repositories) != 1 || !second.Repositories[0].Private || second.NextPage != -1 {
		t.Fatalf("private search lost: %+v", second)
	}
}

func TestUserTokensAreNeverShared(t *testing.T) {
	g := mockClient(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"login":"`+r.Header.Get("Authorization")+`"}`), nil
	})
	for _, id := range []int64{1, 2} {
		u, e := g.CurrentUser(authenticated(id))
		if e != nil || u.Login != fmt.Sprintf("Bearer token-%d", id) {
			t.Fatalf("wrong principal: %+v %v", u, e)
		}
	}
}

func TestPathsAreEncodedAndRedirectsAreNotFollowed(t *testing.T) {
	calls := 0
	g := mockClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.RawQuery != "" || r.URL.Fragment != "" || !strings.Contains(r.URL.EscapedPath(), "%23%3F") {
			t.Fatalf("filename escaped incorrectly: %s", r.URL)
		}
		res := response(302, "")
		res.Header.Set("Location", "https://untrusted.example/secret")
		return res, nil
	})
	if e := g.Get(authenticated(1), "/repos/a/b/contents/name#?.go", "", nil); !IsStatus(e, 302) {
		t.Fatalf("redirect accepted: %v", e)
	}
	if calls != 1 {
		t.Fatal("followed redirect with token")
	}
}

func TestNoContentAndAcceptedResponses(t *testing.T) {
	for _, status := range []int{202, 204} {
		g := mockClient(func(*http.Request) (*http.Response, error) { return response(status, ""), nil })
		if e := g.Do(authenticated(1), "POST", "/anything", nil, nil, nil); e != nil {
			t.Fatal(e)
		}
	}
}

func TestCommitFilesProtectsHeadAndPreservesMode(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	const next = "2222222222222222222222222222222222222222"
	for _, scenario := range []string{"success", "head_changed", "concurrent_push"} {
		t.Run(scenario, func(t *testing.T) {
			mutations := 0
			patches := 0
			g := mockClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					mutations++
				}
				switch {
				case r.Method == "GET" && r.URL.Path == "/repos/a/b":
					return response(200, `{"default_branch":"main","permissions":{"push":true}}`), nil
				case strings.Contains(r.URL.Path, "/branches/"):
					sha := head
					if scenario == "head_changed" {
						sha = next
					}
					return response(200, `{"name":"feature/test","commit":{"sha":"`+sha+`"}}`), nil
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/commits/"):
					return response(200, `{"tree":{"sha":"tree"}}`), nil
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/trees/"):
					return response(200, `{"tree":[{"path":"run.sh","type":"blob","mode":"100755","sha":"old"}]}`), nil
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/trees"):
					var body struct {
						Tree []map[string]any `json:"tree"`
					}
					if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
						t.Fatal(e)
					}
					if body.Tree[0]["mode"] != "100755" || body.Tree[1]["sha"] != nil {
						t.Fatalf("wrong tree: %+v", body)
					}
					return response(201, `{"sha":"new-tree"}`), nil
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/commits"):
					return response(201, `{"sha":"`+next+`"}`), nil
				case r.Method == "PATCH":
					patches++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["force"] != false {
						t.Fatal("force push enabled")
					}
					if scenario == "concurrent_push" {
						return response(422, `{"message":"not fast forward"}`), nil
					}
					return response(200, `{}`), nil
				default:
					t.Fatalf("unexpected call %s %s", r.Method, r.URL)
					return nil, nil
				}
			})
			out, e := g.CommitFiles(authenticated(1), "a", "b", "feature/test", "change", head, []FileChange{{Path: "run.sh", Content: "#!/bin/sh\ntrue\n"}, {Path: "old.go", Delete: true}})
			if scenario == "success" {
				if e != nil || out.SHA != next || mutations != 3 {
					t.Fatalf("commit failed: %+v %v mutations=%d", out, e, mutations)
				}
			} else if e == nil {
				t.Fatal("conflict accepted")
			}
			if scenario == "head_changed" && mutations != 0 {
				t.Fatal("wrote after head conflict")
			}
			if patches > 1 {
				t.Fatal("mutation retried")
			}
		})
	}
}

func TestWritableBranchGuards(t *testing.T) {
	for _, scenario := range []string{"default", "protected", "archived", "read_only"} {
		t.Run(scenario, func(t *testing.T) {
			g := mockClient(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/branches/") {
					return response(200, `{"protected":true}`), nil
				}
				archived := scenario == "archived"
				push := scenario != "read_only"
				return response(200, fmt.Sprintf(`{"default_branch":"main","archived":%t,"permissions":{"push":%t}}`, archived, push)), nil
			})
			branch := "feature"
			if scenario == "default" {
				branch = "main"
			}
			if _, _, e := g.WritableBranch(authenticated(1), "a", "b", branch); e == nil {
				t.Fatal("unsafe write accepted")
			}
		})
	}
}

func TestValidationRejectsTraversalAndInvalidRefs(t *testing.T) {
	for _, path := range []string{"../secret", "/absolute", "a/../b", "a//b", "a\x00b"} {
		if ValidatePath(path, false) == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	for _, ref := range []string{"x..y", "HEAD~1", "a.lock", "a@{b", "a b"} {
		if ValidateRef(ref) == nil {
			t.Fatalf("accepted ref %q", ref)
		}
	}
	if e := ValidatePath("a/name#?.go", false); e != nil {
		t.Fatal(e)
	}
}

func TestRenameFilePreservesExecutableModeAndRefusesDestination(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("destination_exists_%t", exists), func(t *testing.T) {
			mutations := 0
			g := mockClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					mutations++
				}
				switch {
				case r.URL.Path == "/repos/a/b":
					return response(200, `{"default_branch":"main","permissions":{"push":true}}`), nil
				case strings.HasSuffix(r.URL.Path, "/contents/from.sh"):
					return response(200, `{"type":"file","encoding":"base64","content":"dHJ1ZQo=","sha":"blob","path":"from.sh"}`), nil
				case strings.HasSuffix(r.URL.Path, "/contents/to.sh"):
					if exists {
						return response(200, `{"type":"file","encoding":"base64","content":"","sha":"other"}`), nil
					}
					return response(404, `{"message":"not found"}`), nil
				case strings.Contains(r.URL.Path, "/branches/"):
					return response(200, `{"name":"feature","commit":{"sha":"`+head+`"}}`), nil
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/commits/"):
					return response(200, `{"tree":{"sha":"tree"}}`), nil
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/git/trees/"):
					return response(200, `{"tree":[{"path":"from.sh","mode":"100755","type":"blob","sha":"blob"}]}`), nil
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/trees"):
					var body struct {
						Tree []map[string]any `json:"tree"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Tree[1]["path"] != "to.sh" || body.Tree[1]["mode"] != "100755" {
						t.Fatalf("rename changed mode: %+v", body)
					}
					return response(201, `{"sha":"tree-next"}`), nil
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/commits"):
					return response(201, `{"sha":"commit-next"}`), nil
				case r.Method == "PATCH":
					return response(200, `{}`), nil
				default:
					t.Fatalf("unexpected %s %s", r.Method, r.URL)
					return nil, nil
				}
			})
			_, e := g.RenameFile(authenticated(1), "a", "b", "feature", "from.sh", "to.sh", "rename", head)
			if exists {
				if e == nil || mutations != 0 {
					t.Fatalf("destination overwritten: %v %d", e, mutations)
				}
			} else if e != nil || mutations != 3 {
				t.Fatalf("rename failed: %v %d", e, mutations)
			}
		})
	}
}

func TestCommitFilePaginationAndPatchBudget(t *testing.T) {
	g := mockClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/a/b" {
			return response(200, `{}`), nil
		}
		if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "50" {
			t.Fatal("commit page ignored")
		}
		data, _ := json.Marshal(map[string]any{"files": []map[string]any{{"filename": "large.go", "patch": strings.Repeat("x", 5000)}}})
		res := response(200, string(data))
		res.Header.Set("Link", `<https://api.github.com/next>; rel="next"`)
		return res, nil
	})
	out, e := g.Commit(authenticated(1), "a", "b", "main", 2, 50)
	if e != nil {
		t.Fatal(e)
	}
	if out["next_page"] != 3 {
		t.Fatalf("lost continuation: %+v", out)
	}
	file := out["files"].([]any)[0].(map[string]any)
	if file["patch_truncated"] != true || len(file["patch"].(string)) != 4096 {
		t.Fatal("patch budget not enforced")
	}
}
