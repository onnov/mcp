package github

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}
type BranchPage struct {
	Branches     []Branch `json:"branches"`
	NextPage     int      `json:"next_page"`
	ScannedPages int      `json:"scanned_pages"`
}
type Ref struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

func (g *Client) Branch(ctx context.Context, owner, repo, branch string) (Branch, error) {
	var out Branch
	if err := ValidateRepo(owner, repo); err != nil {
		return out, err
	}
	if err := ValidateRef(branch); err != nil {
		return out, err
	}
	err := g.Get(ctx, RepoPath(owner, repo)+"/branches/"+branch, "", &out)
	return out, err
}

func (g *Client) Branches(ctx context.Context, owner, repo, query string, page, limit int) (BranchPage, error) {
	out := BranchPage{Branches: []Branch{}, NextPage: -1}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
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
	for n := 0; n < 5; n++ {
		q.Set("page", strconv.Itoa(page))
		var branches []Branch
		h, err := g.Request(ctx, "GET", RepoPath(owner, repo)+"/branches", q, nil, &branches)
		if err != nil {
			return out, err
		}
		out.ScannedPages++
		for _, b := range branches {
			if needle == "" || strings.Contains(strings.ToLower(b.Name), needle) {
				out.Branches = append(out.Branches, b)
			}
		}
		if HasNext(h) {
			out.NextPage = page + 1
		} else {
			out.NextPage = -1
		}
		if out.NextPage == -1 || len(out.Branches) >= limit || needle == "" {
			break
		}
		page++
	}
	return out, nil
}

func (g *Client) PreferredBranch(ctx context.Context, owner, repo string, r Repository) (string, error) {
	if _, err := g.Branch(ctx, owner, repo, "main"); err == nil {
		return "main", nil
	} else if !IsStatus(err, 404) {
		return "", err
	}
	return r.DefaultBranch, nil
}

func (g *Client) WritableBranch(ctx context.Context, owner, repo, branch string) (Repository, Branch, error) {
	var b Branch
	if err := ValidateRef(branch); err != nil {
		return Repository{}, b, err
	}
	r, err := g.WritableRepository(ctx, owner, repo)
	if err != nil {
		return r, b, err
	}
	if branch == r.DefaultBranch && !g.AllowDefaultBranchWrites {
		return r, b, errors.New("direct writes to the default branch are disabled; create a branch and PR")
	}
	b, err = g.Branch(ctx, owner, repo, branch)
	if err != nil {
		return r, b, err
	}
	if b.Protected {
		return r, b, errors.New("direct writes to protected branches are disabled")
	}
	return r, b, nil
}

func (g *Client) CreateBranch(ctx context.Context, owner, repo, branch, base string) (Ref, error) {
	var out Ref
	r, err := g.WritableRepository(ctx, owner, repo)
	if err != nil {
		return out, err
	}
	if err := ValidateRef(branch); err != nil {
		return out, err
	}
	if base == "" {
		base = r.DefaultBranch
	}
	if err := ValidateRef(base); err != nil {
		return out, err
	}
	var ref Ref
	if err := g.Get(ctx, RepoPath(owner, repo)+"/git/ref/heads/"+base, "", &ref); err != nil {
		return out, err
	}
	err = g.Do(ctx, "POST", RepoPath(owner, repo)+"/git/refs", nil, map[string]string{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}, &out)
	return out, err
}

func (g *Client) DeleteBranch(ctx context.Context, owner, repo, branch, expected string) error {
	if err := ValidateSHA(expected); err != nil {
		return err
	}
	r, b, err := g.WritableBranch(ctx, owner, repo, branch)
	if err != nil {
		return err
	}
	if branch == r.DefaultBranch {
		return errors.New("deleting the default branch is forbidden")
	}
	if b.Commit.SHA != expected {
		return errors.New("branch SHA changed; inspect it before deletion")
	}
	return g.Do(ctx, "DELETE", RepoPath(owner, repo)+"/git/refs/heads/"+branch, nil, nil, nil)
}

func (g *Client) RenameBranch(ctx context.Context, owner, repo, oldName, newName, expected string) (Branch, error) {
	var out Branch
	if err := ValidateSHA(expected); err != nil {
		return out, err
	}
	if err := ValidateRef(newName); err != nil {
		return out, err
	}
	r, b, err := g.WritableBranch(ctx, owner, repo, oldName)
	if err != nil {
		return out, err
	}
	if oldName == r.DefaultBranch {
		return out, errors.New("renaming the default branch is forbidden")
	}
	if b.Commit.SHA != expected {
		return out, errors.New("branch SHA changed; inspect it before renaming")
	}
	err = g.Do(ctx, "POST", RepoPath(owner, repo)+"/branches/"+oldName+"/rename", nil, map[string]string{"new_name": newName}, &out)
	return out, err
}

func (g *Client) Tags(ctx context.Context, owner, repo string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	return g.List(ctx, RepoPath(owner, repo)+"/tags", q, "")
}

func (g *Client) CreateTag(ctx context.Context, owner, repo, tag, sha, message string) (Ref, error) {
	var out Ref
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return out, err
	}
	if err := ValidateRef(tag); err != nil {
		return out, err
	}
	if err := ValidateSHA(sha); err != nil {
		return out, err
	}
	if message != "" {
		if err := ValidateText(message, 4096); err != nil {
			return out, err
		}
		var obj struct {
			SHA string `json:"sha"`
		}
		if err := g.Do(ctx, "POST", RepoPath(owner, repo)+"/git/tags", nil, map[string]string{"tag": tag, "message": message, "object": sha, "type": "commit"}, &obj); err != nil {
			return out, err
		}
		sha = obj.SHA
	}
	err := g.Do(ctx, "POST", RepoPath(owner, repo)+"/git/refs", nil, map[string]string{"ref": "refs/tags/" + tag, "sha": sha}, &out)
	return out, err
}

func (g *Client) DeleteTag(ctx context.Context, owner, repo, tag, expected string) error {
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return err
	}
	if err := ValidateRef(tag); err != nil {
		return err
	}
	if err := ValidateSHA(expected); err != nil {
		return err
	}
	var ref Ref
	if err := g.Get(ctx, RepoPath(owner, repo)+"/git/ref/tags/"+tag, "", &ref); err != nil {
		return err
	}
	if ref.Object.SHA != expected {
		return errors.New("tag SHA changed")
	}
	return g.Do(ctx, "DELETE", RepoPath(owner, repo)+"/git/refs/tags/"+tag, url.Values{}, nil, nil)
}
