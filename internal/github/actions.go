package github

import (
	"context"
	"errors"
	"strconv"
)

func (g *Client) Workflows(ctx context.Context, owner, repo string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	return g.List(ctx, RepoPath(owner, repo)+"/actions/workflows", q, "workflows")
}

func (g *Client) WorkflowRuns(ctx context.Context, owner, repo, branch, status string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	if branch != "" {
		if err := ValidateRef(branch); err != nil {
			return Page{}, err
		}
		q.Set("branch", branch)
	}
	if status != "" {
		if ValidateText(status, 50) != nil {
			return Page{}, errors.New("invalid workflow status")
		}
		q.Set("status", status)
	}
	return g.List(ctx, RepoPath(owner, repo)+"/actions/runs", q, "workflow_runs")
}

func (g *Client) WorkflowRun(ctx context.Context, owner, repo string, runID int) (Object, error) {
	var out Object
	p, err := NumberPath(owner, repo, "actions/runs", runID)
	if err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	err = g.Get(ctx, p, "", &out)
	return out, err
}

func (g *Client) WorkflowJobs(ctx context.Context, owner, repo string, runID, page, limit int) (Page, error) {
	p, err := NumberPath(owner, repo, "actions/runs", runID)
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
	return g.List(ctx, p+"/jobs", q, "jobs")
}

func (g *Client) DispatchWorkflow(ctx context.Context, owner, repo, workflow, ref string, inputs map[string]string) error {
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return err
	}
	if err := ValidateRef(ref); err != nil {
		return err
	}
	if workflow == "" || len(workflow) > 200 || ValidatePath(workflow, false) != nil {
		return errors.New("workflow must be an ID or filename")
	}
	if len(inputs) > 25 {
		return errors.New("maximum 25 workflow inputs")
	}
	for k, v := range inputs {
		if k == "" || ValidateText(k, 100) != nil || ValidateText(v, 4096) != nil {
			return errors.New("invalid workflow input")
		}
	}
	return g.Do(ctx, "POST", RepoPath(owner, repo)+"/actions/workflows/"+workflow+"/dispatches", nil, map[string]any{"ref": ref, "inputs": inputs}, nil)
}

func (g *Client) ChangeWorkflowRun(ctx context.Context, owner, repo string, runID int, action string) error {
	if action != "rerun" && action != "cancel" {
		return errors.New("invalid workflow action")
	}
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return err
	}
	p, err := NumberPath(owner, repo, "actions/runs", runID)
	if err != nil {
		return err
	}
	endpoint := "/rerun"
	if action == "cancel" {
		endpoint = "/cancel"
	}
	return g.Do(ctx, "POST", p+endpoint, nil, nil, nil)
}

func (g *Client) Checks(ctx context.Context, owner, repo, ref, kind string, page, limit int) (Page, error) {
	if err := ValidateRef(ref); err != nil {
		return Page{}, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	suffix, envelope := "/check-runs", "check_runs"
	if kind == "statuses" {
		suffix, envelope = "/statuses", ""
	} else if kind != "checks" {
		return Page{}, errors.New("invalid check kind")
	}
	return g.List(ctx, RepoPath(owner, repo)+"/commits/"+ref+suffix, q, envelope)
}

func (g *Client) Releases(ctx context.Context, owner, repo string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	return g.List(ctx, RepoPath(owner, repo)+"/releases", q, "")
}

func (g *Client) Release(ctx context.Context, owner, repo, tag string) (Object, error) {
	var out Object
	if err := ValidateRef(tag); err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	err := g.Get(ctx, RepoPath(owner, repo)+"/releases/tags/"+tag, "", &out)
	return out, err
}

type ReleaseFields struct {
	TagName              string `json:"tag_name"`
	Target               string `json:"target_commitish,omitempty"`
	Name                 string `json:"name,omitempty"`
	Body                 string `json:"body,omitempty"`
	Draft                bool   `json:"draft"`
	Prerelease           bool   `json:"prerelease"`
	GenerateReleaseNotes bool   `json:"generate_release_notes,omitempty"`
}

func (g *Client) CreateRelease(ctx context.Context, owner, repo string, f ReleaseFields) (Object, error) {
	var out Object
	if err := ValidateRef(f.TagName); err != nil {
		return out, err
	}
	if f.Target != "" {
		if err := ValidateRef(f.Target); err != nil {
			return out, err
		}
	}
	if ValidateText(f.Name, 256) != nil || ValidateText(f.Body, 64<<10) != nil {
		return out, errors.New("release name/body too long")
	}
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return out, err
	}
	err := g.Do(ctx, "POST", RepoPath(owner, repo)+"/releases", nil, f, &out)
	return out, err
}

func (g *Client) DeleteRelease(ctx context.Context, owner, repo string, id int) error {
	if id < 1 {
		return errors.New("release ID must be positive")
	}
	if _, err := g.WritableRepository(ctx, owner, repo); err != nil {
		return err
	}
	return g.Do(ctx, "DELETE", RepoPath(owner, repo)+"/releases/"+strconv.Itoa(id), nil, nil, nil)
}
