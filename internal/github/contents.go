package github

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int    `json:"size"`
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
}
type File struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Size    int    `json:"size"`
	Text    string `json:"text"`
}
type Directory struct {
	Entries         []Entry `json:"entries"`
	AvailableCount  int     `json:"available_count"`
	NextOffset      int     `json:"next_offset"`
	MayBeIncomplete bool    `json:"may_be_incomplete"`
}
type WriteResult struct {
	Path      string `json:"path"`
	BlobSHA   string `json:"blob_sha"`
	CommitSHA string `json:"commit_sha"`
	CommitURL string `json:"commit_url"`
}

func (g *Client) File(ctx context.Context, owner, repo, path, ref string) (File, error) {
	var out File
	if err := ValidatePath(path, false); err != nil {
		return out, err
	}
	if ref != "" {
		if err := ValidateRef(ref); err != nil {
			return out, err
		}
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	var item struct {
		Entry
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := g.Get(ctx, RepoPath(owner, repo)+"/contents/"+path, ref, &item); err != nil {
		return out, err
	}
	if item.Type != "file" || item.Encoding != "base64" {
		return out, errors.New("path must be a regular file with base64 content; directories, symlinks and submodules are unsupported")
	}
	if item.Size > MaxTextBytes {
		return out, errors.New("file exceeds 512 KiB")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(item.Content, "\n", ""))
	if err != nil {
		return out, err
	}
	if err := ValidateText(string(data), MaxTextBytes); err != nil {
		return out, err
	}
	return File{Path: item.Path, SHA: item.SHA, HTMLURL: item.HTMLURL, Size: len(data), Text: string(data)}, nil
}

func (g *Client) Directory(ctx context.Context, owner, repo, path, ref string, offset, limit int) (Directory, error) {
	out := Directory{Entries: []Entry{}, NextOffset: -1}
	if err := ValidatePath(path, true); err != nil {
		return out, err
	}
	if ref != "" {
		if err := ValidateRef(ref); err != nil {
			return out, err
		}
	}
	if limit == 0 {
		limit = 100
	}
	if offset < 0 || limit < 1 || limit > 200 {
		return out, errors.New("offset must be nonnegative; limit 1..200")
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	var entries []Entry
	if err := g.Get(ctx, RepoPath(owner, repo)+"/contents/"+path, ref, &entries); err != nil {
		return out, err
	}
	if offset > len(entries) {
		return out, errors.New("offset exceeds directory length")
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	out.Entries = append(out.Entries, entries[offset:end]...)
	out.AvailableCount = len(entries)
	out.MayBeIncomplete = len(entries) >= 1000
	if end < len(entries) {
		out.NextOffset = end
	}
	return out, nil
}

func (g *Client) WriteFile(ctx context.Context, owner, repo, branch, path, content, message, expected string) (WriteResult, error) {
	var out WriteResult
	if err := ValidatePath(path, false); err != nil {
		return out, err
	}
	if err := ValidateText(content, MaxTextBytes); err != nil {
		return out, err
	}
	if strings.TrimSpace(message) == "" || ValidateText(message, 4096) != nil {
		return out, errors.New("commit message required, maximum 4096 bytes")
	}
	if expected != "" {
		if err := ValidateSHA(expected); err != nil {
			return out, err
		}
	}
	if _, _, err := g.WritableBranch(ctx, owner, repo, branch); err != nil {
		return out, err
	}
	body := map[string]string{"branch": branch, "message": message, "content": base64.StdEncoding.EncodeToString([]byte(content))}
	if expected != "" {
		body["sha"] = expected
	}
	var r struct {
		Content Entry `json:"content"`
		Commit  struct {
			SHA string `json:"sha"`
			URL string `json:"html_url"`
		} `json:"commit"`
	}
	err := g.Do(ctx, "PUT", RepoPath(owner, repo)+"/contents/"+path, nil, body, &r)
	out = WriteResult{Path: r.Content.Path, BlobSHA: r.Content.SHA, CommitSHA: r.Commit.SHA, CommitURL: r.Commit.URL}
	return out, err
}

func (g *Client) DeleteFile(ctx context.Context, owner, repo, branch, path, message, expected string) (WriteResult, error) {
	var out WriteResult
	if err := ValidatePath(path, false); err != nil {
		return out, err
	}
	if err := ValidateSHA(expected); err != nil {
		return out, err
	}
	if strings.TrimSpace(message) == "" || ValidateText(message, 4096) != nil {
		return out, errors.New("commit message required, maximum 4096 bytes")
	}
	if _, _, err := g.WritableBranch(ctx, owner, repo, branch); err != nil {
		return out, err
	}
	var r struct {
		Commit struct {
			SHA string `json:"sha"`
			URL string `json:"html_url"`
		} `json:"commit"`
	}
	err := g.Do(ctx, "DELETE", RepoPath(owner, repo)+"/contents/"+path, nil, map[string]string{"branch": branch, "message": message, "sha": expected}, &r)
	return WriteResult{Path: path, CommitSHA: r.Commit.SHA, CommitURL: r.Commit.URL}, err
}

func (g *Client) Tree(ctx context.Context, owner, repo, ref string, recursive bool) (Object, error) {
	var out Object
	if err := ValidateRef(ref); err != nil {
		return out, err
	}
	if _, err := g.Repository(ctx, owner, repo); err != nil {
		return out, err
	}
	q := url.Values{}
	if recursive {
		q.Set("recursive", "1")
	}
	err := g.Do(ctx, "GET", RepoPath(owner, repo)+"/git/trees/"+ref, q, nil, &out)
	return out, err
}
