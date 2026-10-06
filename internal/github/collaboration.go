package github

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/onnov/mcp/internal/identity"
)

func (g *Client) Accessible(ctx context.Context, owner, repo string) error {
	if _, err := identity.Require(ctx); err != nil {
		return err
	}
	_, err := g.Repository(ctx, owner, repo)
	return err
}

func NumberPath(owner, repo, kind string, number int) (string, error) {
	if err := ValidateRepo(owner, repo); err != nil {
		return "", err
	}
	if number < 1 {
		return "", errors.New("number/ID must be positive")
	}
	return RepoPath(owner, repo) + "/" + kind + "/" + strconv.Itoa(number), nil
}

func (g *Client) Issue(ctx context.Context, owner, repo string, number int) (Object, error) {
	var out Object
	p, err := NumberPath(owner, repo, "issues", number)
	if err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	err = g.Get(ctx, p, "", &out)
	return out, err
}

func (g *Client) Issues(ctx context.Context, owner, repo, state, labels, assignee string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return Page{}, errors.New("state must be open, closed or all")
	}
	if ValidateText(labels, 2048) != nil || ValidateText(assignee, 100) != nil {
		return Page{}, errors.New("filter is too long")
	}
	q.Set("state", state)
	if labels != "" {
		q.Set("labels", labels)
	}
	if assignee != "" {
		q.Set("assignee", assignee)
	}
	return g.List(ctx, RepoPath(owner, repo)+"/issues", q, "")
}

type IssueFields struct {
	Title     *string   `json:"title,omitempty"`
	Body      *string   `json:"body,omitempty"`
	State     *string   `json:"state,omitempty"`
	Labels    *[]string `json:"labels,omitempty"`
	Assignees *[]string `json:"assignees,omitempty"`
}

func (f IssueFields) Validate(create bool) error {
	if create && (f.Title == nil || strings.TrimSpace(*f.Title) == "") {
		return errors.New("title required")
	}
	if f.Title != nil && (strings.TrimSpace(*f.Title) == "" || ValidateText(*f.Title, 256) != nil) {
		return errors.New("title must be nonempty, maximum 256 bytes")
	}
	if f.Body != nil {
		if err := ValidateText(*f.Body, 64<<10); err != nil {
			return err
		}
	}
	if f.State != nil && *f.State != "open" && *f.State != "closed" {
		return errors.New("state must be open or closed")
	}
	for _, list := range []*[]string{f.Labels, f.Assignees} {
		if list != nil {
			if len(*list) > 30 {
				return errors.New("too many labels/assignees")
			}
			for _, s := range *list {
				if err := ValidateText(s, 256); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (g *Client) CreateIssue(ctx context.Context, owner, repo string, fields IssueFields) (Object, error) {
	var out Object
	if err := fields.Validate(true); err != nil {
		return out, err
	}
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	err := g.Do(ctx, "POST", RepoPath(owner, repo)+"/issues", nil, fields, &out)
	return out, err
}

func (g *Client) UpdateIssue(ctx context.Context, owner, repo string, number int, fields IssueFields) (Object, error) {
	var out Object
	if err := fields.Validate(false); err != nil {
		return out, err
	}
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	p, err := NumberPath(owner, repo, "issues", number)
	if err != nil {
		return out, err
	}
	err = g.Do(ctx, "PATCH", p, nil, fields, &out)
	return out, err
}

func (g *Client) Comments(ctx context.Context, owner, repo string, number, page, limit int) (Page, error) {
	p, err := NumberPath(owner, repo, "issues", number)
	if err != nil {
		return Page{}, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	return g.List(ctx, p+"/comments", q, "")
}

func (g *Client) AddComment(ctx context.Context, owner, repo string, number int, body string) (Object, error) {
	var out Object
	if strings.TrimSpace(body) == "" || ValidateText(body, 64<<10) != nil {
		return out, errors.New("comment body required, maximum 64 KiB")
	}
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	p, err := NumberPath(owner, repo, "issues", number)
	if err != nil {
		return out, err
	}
	err = g.Do(ctx, "POST", p+"/comments", nil, map[string]string{"body": body}, &out)
	return out, err
}

func (g *Client) Pull(ctx context.Context, owner, repo string, number int) (Object, error) {
	var out Object
	p, err := NumberPath(owner, repo, "pulls", number)
	if err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	err = g.Get(ctx, p, "", &out)
	return out, err
}

func (g *Client) Pulls(ctx context.Context, owner, repo, state, base, head string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return Page{}, errors.New("state must be open, closed or all")
	}
	q.Set("state", state)
	if base != "" {
		if err := ValidateRef(base); err != nil {
			return Page{}, err
		}
		q.Set("base", base)
	}
	if head != "" {
		if err := validateHead(head); err != nil {
			return Page{}, err
		}
		q.Set("head", head)
	}
	return g.List(ctx, RepoPath(owner, repo)+"/pulls", q, "")
}

func validateHead(head string) error {
	parts := strings.Split(head, ":")
	if len(parts) > 2 {
		return errors.New("head must be branch or owner:branch")
	}
	if len(parts) == 2 {
		if err := ValidateRepo(parts[0], "repo"); err != nil {
			return err
		}
	}
	return ValidateRef(parts[len(parts)-1])
}

func (g *Client) CreatePull(ctx context.Context, owner, repo, head, headRepo, base, title, body string, draft bool) (Object, error) {
	var out Object
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	r, err := g.Repository(ctx, owner, repo)
	if err != nil {
		return out, err
	}
	if base == "" {
		base = r.DefaultBranch
	}
	if err := ValidateRef(base); err != nil {
		return out, err
	}
	if err := validateHead(head); err != nil {
		return out, err
	}
	if base == head {
		return out, errors.New("head and base must differ")
	}
	if strings.TrimSpace(title) == "" || ValidateText(title, 256) != nil || ValidateText(body, 64<<10) != nil {
		return out, errors.New("title required (256 bytes); body maximum 64 KiB")
	}
	f := map[string]any{"head": head, "base": base, "title": title, "body": body, "draft": draft}
	if headRepo != "" {
		if err := ValidateRepo(owner, headRepo); err != nil {
			return out, err
		}
		f["head_repo"] = headRepo
	}
	err = g.Do(ctx, "POST", RepoPath(owner, repo)+"/pulls", nil, f, &out)
	return out, err
}

type PullFields struct {
	Title               *string `json:"title,omitempty"`
	Body                *string `json:"body,omitempty"`
	State               *string `json:"state,omitempty"`
	Base                *string `json:"base,omitempty"`
	MaintainerCanModify *bool   `json:"maintainer_can_modify,omitempty"`
}

func (g *Client) UpdatePull(ctx context.Context, owner, repo string, number int, f PullFields) (Object, error) {
	var out Object
	if err := (IssueFields{Title: f.Title, Body: f.Body, State: f.State}).Validate(false); err != nil {
		return out, err
	}
	if f.Base != nil {
		if err := ValidateRef(*f.Base); err != nil {
			return out, err
		}
	}
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	p, err := NumberPath(owner, repo, "pulls", number)
	if err != nil {
		return out, err
	}
	err = g.Do(ctx, "PATCH", p, nil, f, &out)
	return out, err
}

func (g *Client) PullItems(ctx context.Context, owner, repo, kind string, number, page, limit int) (Page, error) {
	if kind != "files" && kind != "commits" && kind != "reviews" {
		return Page{}, errors.New("invalid PR collection")
	}
	p, err := NumberPath(owner, repo, "pulls", number)
	if err != nil {
		return Page{}, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	result, err := g.List(ctx, p+"/"+kind, q, "")
	if kind == "files" {
		clipPatches(result.Items)
		if effectivePage(q)*effectiveLimit(q) >= 3000 {
			result.Incomplete = true
		}
	}

	return result, err
}

func (g *Client) MergePull(ctx context.Context, owner, repo string, number int, sha, method, title, message string) (Object, error) {
	var out Object
	if err := ValidateSHA(sha); err != nil {
		return out, err
	}
	if method == "" {
		method = "squash"
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return out, errors.New("merge_method must be merge, squash or rebase")
	}
	if ValidateText(title, 256) != nil || ValidateText(message, 64<<10) != nil {
		return out, errors.New("merge text too long")
	}
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return out, err
	}
	p, err := NumberPath(owner, repo, "pulls", number)
	if err != nil {
		return out, err
	}
	f := map[string]string{"sha": sha, "merge_method": method}
	if title != "" {
		f["commit_title"] = title
	}
	if message != "" {
		f["commit_message"] = message
	}
	err = g.Do(ctx, "PUT", p+"/merge", nil, f, &out)
	return out, err
}

func (g *Client) ReviewPull(ctx context.Context, owner, repo string, number int, sha, event, body string) (Object, error) {
	var out Object
	if err := ValidateSHA(sha); err != nil {
		return out, err
	}
	if event != "COMMENT" && event != "APPROVE" && event != "REQUEST_CHANGES" {
		return out, errors.New("event must be COMMENT, APPROVE or REQUEST_CHANGES")
	}
	if ValidateText(body, 64<<10) != nil || ((event == "COMMENT" || event == "REQUEST_CHANGES") && strings.TrimSpace(body) == "") {
		return out, errors.New("review body required for comments/change requests, maximum 64 KiB")
	}
	if err := g.Accessible(ctx, owner, repo); err != nil {
		return out, err
	}
	p, err := NumberPath(owner, repo, "pulls", number)
	if err != nil {
		return out, err
	}
	// Bind the review to the explicitly inspected head revision.
	var pull struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.Get(ctx, p, "", &pull); err != nil {
		return out, err
	}
	if pull.Head.SHA != sha {
		return out, errors.New("PR head changed before review")
	}
	err = g.Do(ctx, "POST", p+"/reviews", nil, map[string]string{"commit_id": sha, "event": event, "body": body}, &out)
	return out, err
}

func effectiveLimit(q map[string][]string) int { n, _ := strconv.Atoi(q["per_page"][0]); return n }

func effectivePage(q map[string][]string) int { n, _ := strconv.Atoi(q["page"][0]); return n }
func clipPatches(items []Object) {
	budget := 64 << 10
	for _, f := range items {
		patch, ok := f["patch"].(string)
		f["patch_available"] = ok
		if ok {
			n := 4096
			if n > budget {
				n = budget
			}
			clipped := Clip(patch, n)
			f["patch"] = clipped
			f["patch_truncated"] = len(clipped) < len(patch)
			budget -= len(clipped)
		}
	}
}
