package tools

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/github"
)

func registerRead(s *mcp.Server, d Services) {
	g := d.GitHub
	add(s, d, "get_repository", "Read metadata and permissions for a public or authorized private repository. Omit owner/repo to use the saved selection.", true, false, func(ctx context.Context, in ReadRepo) (github.Repository, error) {
		o, r, _, e := resolve(ctx, d, in, "")
		if e != nil {
			return github.Repository{}, e
		}
		return g.Repository(ctx, o, r)
	})
	add(s, d, "list_directory", "List a directory at a branch, tag or SHA. Empty path means root. next_offset=-1 means finished; GitHub caps at 1000 entries.", true, false, func(ctx context.Context, in DirectoryInput) (github.Directory, error) {
		o, r, b, e := resolve(ctx, d, in.ReadRepo, in.Ref)
		if e != nil {
			return github.Directory{}, e
		}
		return g.Directory(ctx, o, r, in.Path, b, in.Offset, in.Limit)
	})
	add(s, d, "read_file", "Read one regular UTF-8 file (maximum 512 KiB), returning its exact blob SHA for conflict-safe replacement. Ref defaults to the selected branch.", true, false, func(ctx context.Context, in FileInput) (github.File, error) {
		o, r, b, e := resolve(ctx, d, in.ReadRepo, in.Ref)
		if e != nil {
			return github.File{}, e
		}
		return g.File(ctx, o, r, in.Path, b)
	})
	add(s, d, "get_branch", "Read branch name, protection status and current head SHA. Use this SHA with commit_files or branch deletion.", true, false, func(ctx context.Context, in BranchReadInput) (github.Branch, error) {
		o, r, b, e := resolveRef(ctx, d, in.ReadRepo, in.Branch)
		if e != nil {
			return github.Branch{}, e
		}
		return g.Branch(ctx, o, r, b)
	})
	add(s, d, "list_branches", "List/search branches with pagination. Search scans up to five API pages; follow next_page to continue.", true, false, func(ctx context.Context, in BranchListInput) (github.BranchPage, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.BranchPage{}, e
		}
		return g.Branches(ctx, o, r, in.Query, in.Page, in.Limit)
	})
	add(s, d, "get_tree", "Read Git tree entries for a branch/tag/SHA, optionally recursive. GitHub returns truncated=true if the tree is incomplete.", true, false, func(ctx context.Context, in TreeInput) (ObjectOutput, error) {
		o, r, b, e := resolveRef(ctx, d, in.ReadRepo, in.Ref)
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.Tree(ctx, o, r, b, in.Recursive))
	})
	add(s, d, "list_commits", "List commits on the selected/explicit ref, optionally filtered by file path. Follow next_page.", true, false, func(ctx context.Context, in CommitListInput) (github.Page, error) {
		o, r, b, e := resolve(ctx, d, in.ReadRepo, in.Ref)
		if e != nil {
			return github.Page{}, e
		}
		return g.Commits(ctx, o, r, b, in.Path, in.Page, in.Limit)
	})
	add(s, d, "get_commit", "Read a commit with paginated files and bounded patches. Follow data.next_page and files_may_be_incomplete; use read_file for full text.", true, false, func(ctx context.Context, in CommitReadInput) (ObjectOutput, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.Commit(ctx, o, r, in.Ref, in.Page, in.Limit))
	})
	add(s, d, "compare_refs", "Review a diff between branches/SHAs. File patches are bounded; follow next_offset and inspect patch_truncated/may_be_incomplete.", true, false, func(ctx context.Context, in CompareInput) (github.Comparison, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Comparison{}, e
		}
		return g.Compare(ctx, o, r, in.Base, in.Head, in.Offset, in.Limit)
	})
	add(s, d, "list_tags", "List repository tags with pagination.", true, false, func(ctx context.Context, in RepoPageInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Tags(ctx, o, r, in.Page, in.Limit)
	})
	add(s, d, "list_pull_requests", "List open/closed/all PRs, optionally filtered by base or head. Follow next_page.", true, false, func(ctx context.Context, in PullListInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Pulls(ctx, o, r, in.State, in.Base, in.Head, in.Page, in.Limit)
	})
	add(s, d, "get_pull_request", "Read a PR including base/head revisions, state and mergeability.", true, false, func(ctx context.Context, in NumberInput) (ObjectOutput, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.Pull(ctx, o, r, in.Number))
	})
	for _, kind := range []string{"files", "commits", "reviews"} {
		add(s, d, "list_pull_request_"+kind, "List PR "+kind+" with pagination. File patches may be truncated; do not assume they are complete.", true, false, func(ctx context.Context, in NumberPageInput) (github.Page, error) {
			o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
			if e != nil {
				return github.Page{}, e
			}
			return g.PullItems(ctx, o, r, kind, in.Number, in.Page, in.Limit)
		})
	}
	add(s, d, "list_issues", "List issues and PRs through GitHub's issues API. Entries with pull_request are PRs. Supports state, labels and assignee filters.", true, false, func(ctx context.Context, in IssueListInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Issues(ctx, o, r, in.State, in.Labels, in.Assignee, in.Page, in.Limit)
	})
	add(s, d, "get_issue", "Read one issue or PR through its issue number.", true, false, func(ctx context.Context, in NumberInput) (ObjectOutput, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.Issue(ctx, o, r, in.Number))
	})
	add(s, d, "list_comments", "Read discussion comments on an issue or pull request; follow next_page.", true, false, func(ctx context.Context, in NumberPageInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Comments(ctx, o, r, in.Number, in.Page, in.Limit)
	})
	add(s, d, "list_workflows", "List GitHub Actions workflows.", true, false, func(ctx context.Context, in RepoPageInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Workflows(ctx, o, r, in.Page, in.Limit)
	})
	add(s, d, "list_workflow_runs", "List GitHub Actions runs, optionally filtered by branch/status.", true, false, func(ctx context.Context, in RunsInput) (github.Page, error) {
		o, r, b, e := resolve(ctx, d, in.ReadRepo, in.Branch)
		if e != nil {
			return github.Page{}, e
		}
		return g.WorkflowRuns(ctx, o, r, b, in.Status, in.Page, in.Limit)
	})
	add(s, d, "get_workflow_run", "Read a GitHub Actions run by ID.", true, false, func(ctx context.Context, in RunInput) (ObjectOutput, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.WorkflowRun(ctx, o, r, in.RunID))
	})
	add(s, d, "list_workflow_jobs", "Read jobs and step results for a workflow run.", true, false, func(ctx context.Context, in RunPageInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.WorkflowJobs(ctx, o, r, in.RunID, in.Page, in.Limit)
	})
	for _, kind := range []string{"checks", "statuses"} {
		add(s, d, "list_commit_"+kind, "Read commit "+kind+" on a ref. Follow next_page.", true, false, func(ctx context.Context, in CheckInput) (github.Page, error) {
			o, r, b, e := resolveRef(ctx, d, in.ReadRepo, in.Ref)
			if e != nil {
				return github.Page{}, e
			}
			return g.Checks(ctx, o, r, b, kind, in.Page, in.Limit)
		})
	}
	add(s, d, "list_releases", "List releases visible to the authenticated user, including drafts when permitted.", true, false, func(ctx context.Context, in RepoPageInput) (github.Page, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return github.Page{}, e
		}
		return g.Releases(ctx, o, r, in.Page, in.Limit)
	})
	add(s, d, "get_release", "Read a release by tag name.", true, false, func(ctx context.Context, in ReleaseReadInput) (ObjectOutput, error) {
		o, r, _, e := resolve(ctx, d, in.ReadRepo, "")
		if e != nil {
			return ObjectOutput{}, e
		}
		return object(g.Release(ctx, o, r, in.Tag))
	})
}
