package github

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/onnov/mcp/internal/identity"
)

type Permissions struct {
	Pull  bool `json:"pull"`
	Push  bool `json:"push"`
	Admin bool `json:"admin"`
}
type Repository struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	FullName      string      `json:"full_name"`
	Description   string      `json:"description"`
	HTMLURL       string      `json:"html_url"`
	DefaultBranch string      `json:"default_branch"`
	Language      string      `json:"language"`
	Stars         int         `json:"stargazers_count"`
	Archived      bool        `json:"archived"`
	Private       bool        `json:"private"`
	Permissions   Permissions `json:"permissions"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	URL   string `json:"html_url"`
}

func (g *Client) CurrentUser(ctx context.Context) (User, error) {
	var u User
	if _, err := identity.Require(ctx); err != nil {
		return u, err
	}
	err := g.Get(ctx, "/user", "", &u)
	return u, err
}

func RepoPath(owner, repo string) string { return "/repos/" + owner + "/" + repo }

func (g *Client) Repository(ctx context.Context, owner, repo string) (Repository, error) {
	var r Repository
	if err := ValidateRepo(owner, repo); err != nil {
		return r, err
	}
	if err := g.Get(ctx, RepoPath(owner, repo), "", &r); err != nil {
		return r, err
	}
	if r.Private && identity.From(ctx) == nil {
		return Repository{}, errors.New("private repositories require GitHub OAuth")
	}
	return r, nil
}

func (g *Client) WritableRepository(ctx context.Context, owner, repo string) (Repository, error) {
	if _, err := identity.Require(ctx); err != nil {
		return Repository{}, err
	}
	r, err := g.Repository(ctx, owner, repo)
	if err != nil {
		return r, err
	}
	if r.Archived {
		return r, errors.New("repository is archived")
	}
	if !r.Permissions.Push {
		return r, errors.New("the authenticated GitHub account has no push permission")
	}
	return r, nil
}

type RepositoryPage struct {
	Repositories []Repository `json:"repositories"`
	NextPage     int          `json:"next_page"`
	ScannedPages int          `json:"scanned_pages"`
}

// Search the authenticated user's actual repository list, including collaborations
// and organization memberships. Never use the public /users/{login}/repos list.
// Search scans at most five API pages per request and exposes the continuation.
func (g *Client) Repositories(ctx context.Context, query string, page, limit int) (RepositoryPage, error) {
	out := RepositoryPage{Repositories: []Repository{}, NextPage: -1}
	if _, err := identity.Require(ctx); err != nil {
		return out, err
	}
	if err := ValidateText(query, 256); err != nil {
		return out, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return out, err
	}
	page, _ = strconv.Atoi(q.Get("page"))
	limit, _ = strconv.Atoi(q.Get("per_page"))
	needle := strings.ToLower(strings.TrimSpace(query))
	q.Set("affiliation", "owner,collaborator,organization_member")
	q.Set("visibility", "all")
	q.Set("sort", "full_name")
	q.Set("direction", "asc")
	for n := 0; n < 5; n++ {
		q.Set("page", strconv.Itoa(page))
		var repos []Repository
		headers, err := g.Request(ctx, "GET", "/user/repos", q, nil, &repos)
		if err != nil {
			return out, err
		}
		out.ScannedPages++
		for _, r := range repos {
			if needle == "" || strings.Contains(strings.ToLower(r.FullName+" "+r.Description), needle) {
				out.Repositories = append(out.Repositories, r)
			}
		}
		if HasNext(headers) {
			out.NextPage = page + 1
		} else {
			out.NextPage = -1
		}
		// Each API page uses the requested limit; never drop matches within a page.
		if out.NextPage == -1 || len(out.Repositories) >= limit || needle == "" {
			break
		}
		page++
	}
	return out, nil
}

func (g *Client) Search(ctx context.Context, owner, repo, query, kind string, page, limit int) (Page, error) {
	if _, err := identity.Require(ctx); err != nil {
		return Page{}, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(query) == "" || ValidateText(query, 1024) != nil {
		return Page{}, errors.New("search query is required, maximum 1024 bytes")
	}
	// A selected repository is always the search boundary.
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if strings.HasPrefix(term, "repo:") || strings.HasPrefix(term, "org:") || strings.HasPrefix(term, "user:") {
			return Page{}, errors.New("repository/org/user qualifiers are supplied by the server")
		}
	}
	if kind != "code" && kind != "issues" {
		return Page{}, errors.New("invalid search kind")
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	q.Set("q", query+" repo:"+owner+"/"+repo)
	return g.List(ctx, "/search/"+kind, q, "items")
}

func (g *Client) CreateRepository(ctx context.Context, name, org, description string, private bool) (Repository, error) {
	var out Repository
	if _, err := identity.Require(ctx); err != nil {
		return out, err
	}
	owner := org
	if owner == "" {
		owner = "user"
	}
	if err := ValidateRepo(owner, name); err != nil {
		return out, err
	}
	if err := ValidateText(description, 4096); err != nil {
		return out, err
	}
	path := "/user/repos"
	if org != "" {
		path = "/orgs/" + org + "/repos"
	}
	err := g.Do(ctx, "POST", path, nil, map[string]any{"name": name, "description": description, "private": private, "auto_init": true}, &out)
	return out, err
}

func (g *Client) Fork(ctx context.Context, owner, repo, org, name string) (Repository, error) {
	var out Repository
	if _, err := identity.Require(ctx); err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	body := map[string]any{}
	if org != "" {
		if err := ValidateRepo(org, repo); err != nil {
			return out, err
		}
		body["organization"] = org
	}
	if name != "" {
		if err := ValidateRepo(owner, name); err != nil {
			return out, err
		}
		body["name"] = name
	}
	err := g.Do(ctx, "POST", RepoPath(owner, repo)+"/forks", url.Values{}, body, &out)
	return out, err
}
