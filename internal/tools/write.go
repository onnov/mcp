package tools

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/github"
)

type BranchResult struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

func registerWrite(s *mcp.Server, d Services) {
	g := d.GitHub
	add(s, d, "create_branch", "Create a new branch from base_branch (default: repository default). Existing branches are never overwritten.", false, false, func(ctx context.Context, in BranchInput) (BranchResult, error) {
		r, e := g.CreateBranch(ctx, in.Owner, in.Repo, in.Branch, in.BaseBranch)
		return BranchResult{Branch: in.Branch, SHA: r.Object.SHA}, e
	})
	add(s, d, "write_file", "Create/replace a UTF-8 file and commit immediately. For replacements supply expected_sha from the same branch; omit only for a new file. No blind retries.", false, true, func(ctx context.Context, in WriteInput) (github.WriteResult, error) {
		return g.WriteFile(ctx, in.Owner, in.Repo, in.Branch, in.Path, in.Content, in.Message, in.ExpectedSHA)
	})
	add(s, d, "commit_files", "Commit 1..50 text file additions/replacements/deletions atomically. Require expected_head_sha from get_branch. Non-fast-forward updates fail; no force push.", false, true, func(ctx context.Context, in CommitFilesInput) (github.CommitResult, error) {
		return g.CommitFiles(ctx, in.Owner, in.Repo, in.Branch, in.Message, in.ExpectedHeadSHA, in.Files)
	})
	add(s, d, "delete_file", "Delete one file and commit. Require its current blob SHA and an explicit branch.", false, true, func(ctx context.Context, in DeleteInput) (github.WriteResult, error) {
		return g.DeleteFile(ctx, in.Owner, in.Repo, in.Branch, in.Path, in.Message, in.ExpectedSHA)
	})
	add(s, d, "rename_file", "Rename one regular UTF-8 file in a single commit. Destination must not exist. Require expected_head_sha from get_branch.", false, true, func(ctx context.Context, in RenameInput) (github.CommitResult, error) {
		return g.RenameFile(ctx, in.Owner, in.Repo, in.Branch, in.From, in.To, in.Message, in.ExpectedHeadSHA)
	})
	add(s, d, "delete_branch", "Delete a non-default, unprotected branch after checking its current expected_sha. GitHub's delete API has no atomic compare-and-delete; inspect before use.", false, true, func(ctx context.Context, in BranchDeleteInput) (Done, error) {
		return done(g.DeleteBranch(ctx, in.Owner, in.Repo, in.Branch, in.ExpectedSHA))
	})
	add(s, d, "rename_branch", "Rename a non-default, unprotected branch after checking expected_sha. GitHub's rename API has no atomic SHA precondition.", false, true, func(ctx context.Context, in BranchRenameInput) (github.Branch, error) {
		return g.RenameBranch(ctx, in.Owner, in.Repo, in.Branch, in.NewName, in.ExpectedSHA)
	})
	add(s, d, "create_tag", "Create a lightweight or annotated tag pointing at an explicit commit SHA. Existing tags are never replaced.", false, false, func(ctx context.Context, in TagInput) (github.Ref, error) {
		return g.CreateTag(ctx, in.Owner, in.Repo, in.Tag, in.SHA, in.Message)
	})
	add(s, d, "delete_tag", "Delete a tag only after comparing its ref object SHA with expected_sha. GitHub's delete API has no atomic precondition.", false, true, func(ctx context.Context, in TagDeleteInput) (Done, error) {
		return done(g.DeleteTag(ctx, in.Owner, in.Repo, in.Tag, in.ExpectedSHA))
	})
	add(s, d, "create_repository", "Create and initialize a repository for the current user or organization. Private by default; GitHub checks organization permissions.", false, false, func(ctx context.Context, in CreateRepoInput) (github.Repository, error) {
		private := true
		if in.Private != nil {
			private = *in.Private
		}
		return g.CreateRepository(ctx, in.Name, in.Organization, in.Description, private)
	})
	add(s, d, "fork_repository", "Fork an accessible repository to the current user or organization. GitHub creates forks asynchronously; inspect readiness before editing.", false, false, func(ctx context.Context, in ForkInput) (github.Repository, error) {
		return g.Fork(ctx, in.Owner, in.Repo, in.Organization, in.Name)
	})
	add(s, d, "create_pull_request", "Open a PR or draft from a branch or fork-owner:branch. Does not merge. Same-organization forks can supply head_repo.", false, false, func(ctx context.Context, in PullCreateInput) (ObjectOutput, error) {
		return object(g.CreatePull(ctx, in.Owner, in.Repo, in.Head, in.HeadRepo, in.Base, in.Title, in.Body, in.Draft))
	})
	add(s, d, "update_pull_request", "Update explicit PR fields; state can be open/closed. Does not merge or convert draft status.", false, true, func(ctx context.Context, in PullUpdateInput) (ObjectOutput, error) {
		return object(g.UpdatePull(ctx, in.Owner, in.Repo, in.Number, in.PullFields))
	})
	add(s, d, "merge_pull_request", "Merge a reviewed PR using squash/merge/rebase. Require the exact inspected expected_head_sha. GitHub enforces checks and branch rules.", false, true, func(ctx context.Context, in PullMergeInput) (ObjectOutput, error) {
		return object(g.MergePull(ctx, in.Owner, in.Repo, in.Number, in.ExpectedHeadSHA, in.MergeMethod, in.CommitTitle, in.CommitMessage))
	})
	add(s, d, "submit_pull_request_review", "Submit COMMENT, APPROVE or REQUEST_CHANGES on an explicitly inspected commit_id. This publishes a review as the connected GitHub user.", false, false, func(ctx context.Context, in ReviewInput) (ObjectOutput, error) {
		return object(g.ReviewPull(ctx, in.Owner, in.Repo, in.Number, in.CommitID, in.Event, in.Body))
	})
	add(s, d, "create_issue", "Create an issue with a title, body, labels and assignees. GitHub checks each requested field's permissions.", false, false, func(ctx context.Context, in IssueCreateInput) (ObjectOutput, error) {
		fields := github.IssueFields{Title: &in.Title, Body: &in.Body}
		if in.Labels != nil {
			fields.Labels = &in.Labels
		}
		if in.Assignees != nil {
			fields.Assignees = &in.Assignees
		}
		return object(g.CreateIssue(ctx, in.Owner, in.Repo, fields))
	})
	add(s, d, "update_issue", "Update an issue or PR's issue fields. A provided empty labels/assignees list clears that list; omitted fields are unchanged.", false, true, func(ctx context.Context, in IssueUpdateInput) (ObjectOutput, error) {
		return object(g.UpdateIssue(ctx, in.Owner, in.Repo, in.Number, in.IssueFields))
	})
	add(s, d, "add_comment", "Publish a discussion comment on an issue or PR as the connected user.", false, false, func(ctx context.Context, in CommentInput) (ObjectOutput, error) {
		return object(g.AddComment(ctx, in.Owner, in.Repo, in.Number, in.Body))
	})
	add(s, d, "dispatch_workflow", "Trigger a workflow_dispatch workflow at an explicit ref with named inputs. Does not run a local shell.", false, false, func(ctx context.Context, in DispatchInput) (Done, error) {
		return done(g.DispatchWorkflow(ctx, in.Owner, in.Repo, in.Workflow, in.Ref, in.Inputs))
	})
	add(s, d, "rerun_workflow_run", "Rerun a GitHub Actions run by ID.", false, false, func(ctx context.Context, in RunWriteInput) (Done, error) {
		return done(g.ChangeWorkflowRun(ctx, in.Owner, in.Repo, in.RunID, "rerun"))
	})
	add(s, d, "cancel_workflow_run", "Cancel a GitHub Actions run by ID.", false, true, func(ctx context.Context, in RunWriteInput) (Done, error) {
		return done(g.ChangeWorkflowRun(ctx, in.Owner, in.Repo, in.RunID, "cancel"))
	})
	add(s, d, "create_release", "Create a GitHub release, optionally a draft/prerelease. Publishing makes release information visible to repository users.", false, false, func(ctx context.Context, in ReleaseCreateInput) (ObjectOutput, error) {
		return object(g.CreateRelease(ctx, in.Owner, in.Repo, in.ReleaseFields))
	})
	add(s, d, "delete_release", "Delete a release by ID. Does not delete the associated Git tag.", false, true, func(ctx context.Context, in ReleaseDeleteInput) (Done, error) {
		return done(g.DeleteRelease(ctx, in.Owner, in.Repo, in.ReleaseID))
	})
}
