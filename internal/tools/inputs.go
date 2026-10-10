// Package tools adapts explicit, typed MCP tools to application services.
package tools

import "github.com/onnov/mcp/internal/github"

// Every input embeds ChatInput exactly once, directly or through ReadRepo or
// WriteRepo, so each tool accepts chat.
type Empty struct {
	ChatInput
}
type ReadRepo struct {
	ChatInput
	Owner string `json:"owner,omitempty" jsonschema:"GitHub owner; omit together with repo to use this chat's repository"`
	Repo  string `json:"repo,omitempty" jsonschema:"Repository name, not a URL"`
}
type WriteRepo struct {
	ChatInput
	Owner string `json:"owner" jsonschema:"Explicit GitHub owner; writes never infer this from a saved selection"`
	Repo  string `json:"repo" jsonschema:"Explicit repository name"`
}
type Pagination struct {
	Page  int `json:"page,omitempty" jsonschema:"Page number; default 1. Use next_page; -1 means finished"`
	Limit int `json:"limit,omitempty" jsonschema:"API page size; default 50, maximum 100"`
}
type RepoPageInput struct {
	ReadRepo
	Pagination
}
type QueryInput struct {
	ChatInput
	Query string `json:"query,omitempty"`
	Pagination
}
type BranchListInput struct {
	ReadRepo
	Pagination
	Query string `json:"query,omitempty"`
}
type SelectionInput struct {
	WriteRepo
	Branch string `json:"branch,omitempty" jsonschema:"Reuse the last branch for this repository; initially main when present, otherwise its default branch"`
}
type FileInput struct {
	ReadRepo
	Path string `json:"path"`
	Ref  string `json:"ref,omitempty"`
}
type DirectoryInput struct {
	ReadRepo
	Path   string `json:"path,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type BranchInput struct {
	WriteRepo
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch,omitempty"`
}
type BranchReadInput struct {
	ReadRepo
	Branch string `json:"branch,omitempty"`
}
type BranchDeleteInput struct {
	WriteRepo
	Branch      string `json:"branch"`
	ExpectedSHA string `json:"expected_sha"`
}
type BranchRenameInput struct {
	WriteRepo
	Branch      string `json:"branch"`
	NewName     string `json:"new_name"`
	ExpectedSHA string `json:"expected_sha"`
}
type WriteInput struct {
	WriteRepo
	Branch      string `json:"branch"`
	Path        string `json:"path"`
	Content     string `json:"content" jsonschema:"Full new UTF-8 text, not a patch; maximum 512 KiB"`
	Message     string `json:"message"`
	ExpectedSHA string `json:"expected_sha,omitempty" jsonschema:"Blob SHA from read_file on this branch; omit only to create a new file"`
}
type DeleteInput struct {
	WriteRepo
	Branch      string `json:"branch"`
	Path        string `json:"path"`
	Message     string `json:"message"`
	ExpectedSHA string `json:"expected_sha"`
}
type RenameInput struct {
	WriteRepo
	Branch          string `json:"branch"`
	From            string `json:"from"`
	To              string `json:"to"`
	Message         string `json:"message"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
}
type CommitFilesInput struct {
	WriteRepo
	Branch          string              `json:"branch"`
	Message         string              `json:"message"`
	ExpectedHeadSHA string              `json:"expected_head_sha"`
	Files           []github.FileChange `json:"files" jsonschema:"1..50 file changes; combined content at most 2 MiB"`
}
type TreeInput struct {
	ReadRepo
	Ref       string `json:"ref,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
}
type CommitListInput struct {
	ReadRepo
	Pagination
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"`
}
type CommitReadInput struct {
	ReadRepo
	Pagination
	Ref string `json:"ref"`
}
type CompareInput struct {
	ReadRepo
	Base   string `json:"base"`
	Head   string `json:"head"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type TagInput struct {
	WriteRepo
	Tag     string `json:"tag"`
	SHA     string `json:"sha"`
	Message string `json:"message,omitempty" jsonschema:"Nonempty creates an annotated tag; empty creates a lightweight tag"`
}
type TagDeleteInput struct {
	WriteRepo
	Tag         string `json:"tag"`
	ExpectedSHA string `json:"expected_sha"`
}
type SearchInput struct {
	ReadRepo
	Pagination
	Query string `json:"query"`
}
type CreateRepoInput struct {
	ChatInput
	Name         string `json:"name"`
	Organization string `json:"organization,omitempty"`
	Description  string `json:"description,omitempty"`
	Private      *bool  `json:"private,omitempty" jsonschema:"Default true"`
}
type ForkInput struct {
	WriteRepo
	Organization string `json:"organization,omitempty"`
	Name         string `json:"name,omitempty"`
}
type NumberInput struct {
	ReadRepo
	Number int `json:"number"`
}
type NumberPageInput struct {
	ReadRepo
	Pagination
	Number int `json:"number"`
}
type PullListInput struct {
	ReadRepo
	Pagination
	State string `json:"state,omitempty"`
	Base  string `json:"base,omitempty"`
	Head  string `json:"head,omitempty"`
}
type PullCreateInput struct {
	WriteRepo
	Head     string `json:"head" jsonschema:"Branch or fork-owner:branch"`
	HeadRepo string `json:"head_repo,omitempty"`
	Base     string `json:"base,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Draft    bool   `json:"draft,omitempty"`
}
type PullUpdateInput struct {
	WriteRepo
	Number int `json:"number"`
	github.PullFields
}
type PullMergeInput struct {
	WriteRepo
	Number          int    `json:"number"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	MergeMethod     string `json:"merge_method,omitempty" jsonschema:"squash (default), merge or rebase"`
	CommitTitle     string `json:"commit_title,omitempty"`
	CommitMessage   string `json:"commit_message,omitempty"`
}
type ReviewInput struct {
	WriteRepo
	Number   int    `json:"number"`
	CommitID string `json:"commit_id"`
	Event    string `json:"event" jsonschema:"COMMENT, APPROVE or REQUEST_CHANGES"`
	Body     string `json:"body,omitempty"`
}
type IssueListInput struct {
	ReadRepo
	Pagination
	State    string `json:"state,omitempty"`
	Labels   string `json:"labels,omitempty"`
	Assignee string `json:"assignee,omitempty"`
}
type IssueCreateInput struct {
	WriteRepo
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
}
type IssueUpdateInput struct {
	WriteRepo
	Number int `json:"number"`
	github.IssueFields
}
type CommentInput struct {
	WriteRepo
	Number int    `json:"number" jsonschema:"Issue or pull request number"`
	Body   string `json:"body"`
}
type RunsInput struct {
	ReadRepo
	Pagination
	Branch string `json:"branch,omitempty"`
	Status string `json:"status,omitempty"`
}
type RunInput struct {
	ReadRepo
	RunID int `json:"run_id"`
}
type RunPageInput struct {
	ReadRepo
	Pagination
	RunID int `json:"run_id"`
}
type RunWriteInput struct {
	WriteRepo
	RunID int `json:"run_id"`
}
type DispatchInput struct {
	WriteRepo
	Workflow string            `json:"workflow" jsonschema:"Workflow filename or ID"`
	Ref      string            `json:"ref"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}
type CheckInput struct {
	ReadRepo
	Pagination
	Ref string `json:"ref,omitempty"`
}
type ReleaseReadInput struct {
	ReadRepo
	Tag string `json:"tag"`
}
type ReleaseCreateInput struct {
	WriteRepo
	github.ReleaseFields
}
type ReleaseDeleteInput struct {
	WriteRepo
	ReleaseID int `json:"release_id"`
}
type ObjectOutput struct {
	Data github.Object `json:"data"`
}
type Done struct {
	OK bool `json:"ok"`
}
