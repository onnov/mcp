package github

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type FileChange struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	Delete     bool   `json:"delete,omitempty"`
	Executable *bool  `json:"executable,omitempty" jsonschema:"Omit to preserve an existing file mode; true means executable"`
}
type CommitResult struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	TreeSHA string `json:"tree_sha"`
	Branch  string `json:"branch"`
}

// CommitFiles creates one commit for all changes and fast-forwards a branch.
// It never force-pushes. If an external writer advances the branch concurrently,
// the final non-fast-forward update fails rather than overwriting their commit.
func (g *Client) CommitFiles(ctx context.Context, owner, repo, branch, message, expected string, changes []FileChange) (CommitResult, error) {
	var out CommitResult
	if err := ValidateSHA(expected); err != nil {
		return out, err
	}
	if strings.TrimSpace(message) == "" || ValidateText(message, 4096) != nil {
		return out, errors.New("commit message required, maximum 4096 bytes")
	}
	if len(changes) < 1 || len(changes) > 50 {
		return out, errors.New("commit requires 1..50 file changes")
	}
	seen := map[string]bool{}
	total := 0
	entries := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		if err := ValidatePath(change.Path, false); err != nil {
			return out, err
		}
		if seen[change.Path] {
			return out, errors.New("duplicate path in commit")
		}
		seen[change.Path] = true
		mode := "100644"
		if change.Executable != nil && *change.Executable {
			mode = "100755"
		}
		e := map[string]any{"path": change.Path, "mode": mode, "type": "blob"}
		if change.Delete {
			if change.Content != "" || change.Executable != nil {
				return out, errors.New("deletion must not include content or executable mode")
			}
			e["sha"] = nil
		} else {
			if err := ValidateText(change.Content, MaxTextBytes); err != nil {
				return out, err
			}
			total += len(change.Content)
			e["content"] = change.Content
		}
		entries = append(entries, e)
	}
	if total > MaxCommitBytes {
		return out, errors.New("combined commit contents exceed 2 MiB")
	}
	_, b, err := g.WritableBranch(ctx, owner, repo, branch)
	if err != nil {
		return out, err
	}
	if b.Commit.SHA != expected {
		return out, errors.New("branch head changed; inspect changes and retry with the current SHA")
	}
	base := RepoPath(owner, repo)
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.Get(ctx, base+"/git/commits/"+expected, "", &parent); err != nil {
		return out, err
	}
	cache := map[string][]gitTreeEntry{}
	for i, change := range changes {
		if change.Delete {
			continue
		}
		entry, exists, e := g.lookupTree(ctx, base, parent.Tree.SHA, change.Path, cache)
		if e != nil {
			return out, e
		}
		if exists && (entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755")) {
			return out, errors.New("only regular files can be replaced")
		}
		if exists && change.Executable == nil {
			entries[i]["mode"] = entry.Mode
		}
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := g.Do(ctx, "POST", base+"/git/trees", nil, map[string]any{"base_tree": parent.Tree.SHA, "tree": entries}, &tree); err != nil {
		return out, err
	}
	var commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.Do(ctx, "POST", base+"/git/commits", nil, map[string]any{"message": message, "tree": tree.SHA, "parents": []string{expected}}, &commit); err != nil {
		return out, err
	}
	if err := g.Do(ctx, "PATCH", base+"/git/refs/heads/"+branch, nil, map[string]any{"sha": commit.SHA, "force": false}, nil); err != nil {
		return out, err
	}
	return CommitResult{SHA: commit.SHA, HTMLURL: commit.HTMLURL, TreeSHA: tree.SHA, Branch: branch}, nil
}

func (g *Client) RenameFile(ctx context.Context, owner, repo, branch, from, to, message, expected string) (CommitResult, error) {
	if from == to {
		return CommitResult{}, errors.New("source and destination must differ")
	}
	if err := ValidatePath(from, false); err != nil {
		return CommitResult{}, err
	}
	if err := ValidateSHA(expected); err != nil {
		return CommitResult{}, err
	}
	if err := ValidatePath(to, false); err != nil {
		return CommitResult{}, err
	}
	file, err := g.File(ctx, owner, repo, from, branch)
	if err != nil {
		return CommitResult{}, err
	}
	// Avoid overwriting an existing destination. The expected branch head also
	// protects against a destination created between this check and ref update.
	if _, err := g.File(ctx, owner, repo, to, branch); err == nil {
		return CommitResult{}, errors.New("destination already exists")
	} else if !IsStatus(err, 404) {
		return CommitResult{}, err
	}
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.Get(ctx, RepoPath(owner, repo)+"/git/commits/"+expected, "", &parent); err != nil {
		return CommitResult{}, err
	}
	entry, exists, err := g.lookupTree(ctx, RepoPath(owner, repo), parent.Tree.SHA, from, map[string][]gitTreeEntry{})
	if err != nil {
		return CommitResult{}, err
	}
	if !exists || entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.SHA != file.SHA {
		return CommitResult{}, errors.New("source is not a regular file at the expected revision")
	}
	executable := entry.Mode == "100755"
	return g.CommitFiles(ctx, owner, repo, branch, message, expected, []FileChange{{Path: from, Delete: true}, {Path: to, Content: file.Text, Executable: &executable}})
}

func (g *Client) Commits(ctx context.Context, owner, repo, ref, path string, page, limit int) (Page, error) {
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return Page{}, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return Page{}, err
	}
	if ref != "" {
		if err := ValidateRef(ref); err != nil {
			return Page{}, err
		}
		q.Set("sha", ref)
	}
	if path != "" {
		if err := ValidatePath(path, false); err != nil {
			return Page{}, err
		}
		q.Set("path", path)
	}
	return g.List(ctx, RepoPath(owner, repo)+"/commits", q, "")
}

func (g *Client) Commit(ctx context.Context, owner, repo, ref string, page, limit int) (Object, error) {
	var out Object
	if err := ValidateRef(ref); err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	q, err := PageQuery(page, limit)
	if err != nil {
		return out, err
	}
	headers, err := g.Request(ctx, "GET", RepoPath(owner, repo)+"/commits/"+ref, q, nil, &out)
	if err != nil {
		return out, err
	}
	out["next_page"] = -1
	if HasNext(headers) {
		n, _ := strconv.Atoi(q.Get("page"))
		out["next_page"] = n + 1
	}
	out["files_may_be_incomplete"] = effectivePage(q)*effectiveLimit(q) >= 3000
	if files, ok := out["files"].([]any); ok {
		items := make([]Object, 0, len(files))
		for _, f := range files {
			if item, ok := f.(map[string]any); ok {
				items = append(items, Object(item))
			}
		}
		clipPatches(items)
	}
	return out, nil
}

type DiffFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
	PatchAvailable   bool   `json:"patch_available"`
	PatchTruncated   bool   `json:"patch_truncated"`
}
type Comparison struct {
	URL             string     `json:"html_url"`
	Status          string     `json:"status"`
	AheadBy         int        `json:"ahead_by"`
	BehindBy        int        `json:"behind_by"`
	Files           []DiffFile `json:"files"`
	AvailableCount  int        `json:"available_count"`
	NextOffset      int        `json:"next_offset"`
	MayBeIncomplete bool       `json:"may_be_incomplete"`
}

func (g *Client) Compare(ctx context.Context, owner, repo, base, head string, offset, limit int) (Comparison, error) {
	out := Comparison{Files: []DiffFile{}, NextOffset: -1}
	if err := ValidateRef(base); err != nil {
		return out, err
	}
	if err := ValidateRef(head); err != nil {
		return out, err
	}
	if limit == 0 {
		limit = 20
	}
	if offset < 0 || limit < 1 || limit > 50 {
		return out, errors.New("offset must be nonnegative; limit 1..50")
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	var r struct {
		URL      string `json:"html_url"`
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
		Files    []struct {
			Filename         string  `json:"filename"`
			PreviousFilename string  `json:"previous_filename"`
			Status           string  `json:"status"`
			Additions        int     `json:"additions"`
			Deletions        int     `json:"deletions"`
			Patch            *string `json:"patch"`
		} `json:"files"`
	}
	if err := g.Do(ctx, "GET", RepoPath(owner, repo)+"/compare/"+base+"..."+head, url.Values{"page": {"1"}, "per_page": {"1"}}, nil, &r); err != nil {
		return out, err
	}
	if offset > len(r.Files) {
		return out, errors.New("offset exceeds available files")
	}
	end := offset + limit
	if end > len(r.Files) {
		end = len(r.Files)
	}
	out.URL = r.URL
	out.Status = r.Status
	out.AheadBy = r.AheadBy
	out.BehindBy = r.BehindBy
	out.AvailableCount = len(r.Files)
	out.MayBeIncomplete = len(r.Files) >= 300
	budget := 64 << 10
	for _, f := range r.Files[offset:end] {
		d := DiffFile{Filename: f.Filename, PreviousFilename: f.PreviousFilename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, PatchAvailable: f.Patch != nil}
		if f.Patch != nil {
			n := 4096
			if n > budget {
				n = budget
			}
			d.Patch = Clip(*f.Patch, n)
			d.PatchTruncated = len(d.Patch) < len(*f.Patch)
			budget -= len(d.Patch)
		}
		out.Files = append(out.Files, d)
	}
	if end < len(r.Files) {
		out.NextOffset = end
	}
	return out, nil
}

// Walk only the needed directories, caching shared subtrees. Recursive trees may
// be truncated for large repositories, so they are unsuitable for write guards.
type gitTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (g *Client) lookupTree(ctx context.Context, base, sha, path string, cache map[string][]gitTreeEntry) (gitTreeEntry, bool, error) {
	parts := strings.Split(path, "/")
	for i, name := range parts {
		entries, ok := cache[sha]
		if !ok {
			var tree struct {
				Entries   []gitTreeEntry `json:"tree"`
				Truncated bool           `json:"truncated"`
			}
			if err := g.Get(ctx, base+"/git/trees/"+sha, "", &tree); err != nil {
				return gitTreeEntry{}, false, err
			}
			if tree.Truncated {
				return gitTreeEntry{}, false, errors.New("directory tree was truncated; refusing an incomplete write guard")
			}
			entries = tree.Entries
			cache[sha] = entries
		}
		found := false
		for _, entry := range entries {
			if entry.Path != name {
				continue
			}
			found = true
			if i == len(parts)-1 {
				return entry, true, nil
			}
			if entry.Type != "tree" {
				return gitTreeEntry{}, false, errors.New("a parent path is not a directory")
			}
			sha = entry.SHA
			break
		}
		if !found {
			return gitTreeEntry{}, false, nil
		}
	}
	return gitTreeEntry{}, false, nil
}
