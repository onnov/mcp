// Package workspace combines per-user preferences with live GitHub permissions.
package workspace

import (
	"context"
	"errors"
	"strings"

	"github.com/onnov/mcp/internal/github"
	"github.com/onnov/mcp/internal/identity"
	"github.com/onnov/mcp/internal/preferences"
)

type Service struct {
	GitHub      *github.Client
	Preferences *preferences.Store
}
type SelectionResult struct {
	Selection   preferences.Selection `json:"selection"`
	Available   bool                  `json:"available"`
	Message     string                `json:"message"`
	Permissions github.Permissions    `json:"permissions"`
}

func (s *Service) Selection(ctx context.Context) (SelectionResult, error) {
	var out SelectionResult
	p, err := identity.Require(ctx)
	if err != nil {
		return out, err
	}
	v, ok := s.Preferences.Get(p.UserID)
	if !ok {
		out.Message = "Select a repository to start"
		return out, nil
	}
	r, err := s.GitHub.Repository(ctx, v.Owner, v.Repo)
	if github.IsStatus(err, 404) || github.IsStatus(err, 403) {
		out.Message = "The previous repository is no longer accessible; select another"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if v.RepositoryID != r.ID {
		out.Message = "Repository identity changed; select it again"
		return out, nil
	}
	if _, err := s.GitHub.Branch(ctx, v.Owner, v.Repo, v.Branch); err != nil {
		if !github.IsStatus(err, 404) {
			return out, err
		}
		previousBranch := v.Branch
		v.Branch, err = s.GitHub.PreferredBranch(ctx, v.Owner, v.Repo, r)
		if err != nil {
			return out, err
		}
		out.Message = "Previous branch " + previousBranch + " no longer exists; using " + v.Branch + ". Select it to save the new choice."
		if v.Branch == "" {
			out.Message = "Repository has no branches yet"
			return out, nil
		}
	}
	out.Selection = v
	out.Available = true
	out.Permissions = r.Permissions
	return out, nil
}

func (s *Service) Select(ctx context.Context, owner, repo, branch string) (SelectionResult, error) {
	var out SelectionResult
	p, err := identity.Require(ctx)
	if err != nil {
		return out, err
	}
	r, err := s.GitHub.Repository(ctx, owner, repo)
	if err != nil {
		return out, err
	}
	if branch == "" {
		if previous, ok := s.Preferences.Repository(p.UserID, r.ID); ok {
			if _, e := s.GitHub.Branch(ctx, owner, repo, previous.Branch); e == nil {
				branch = previous.Branch
			} else if !github.IsStatus(e, 404) {
				return out, e
			}
		}
		if branch == "" {
			branch, err = s.GitHub.PreferredBranch(ctx, owner, repo, r)
			if err != nil {
				return out, err
			}
		}
	}
	if branch == "" {
		return out, errors.New("repository has no branches; initialize it first")
	}
	if _, err := s.GitHub.Branch(ctx, owner, repo, branch); err != nil {
		return out, err
	}
	v := preferences.Selection{Owner: r.Owner.Login, Repo: r.Name, Branch: branch, RepositoryID: r.ID}
	// Test doubles and older GitHub-compatible endpoints can omit name/owner.
	if v.Owner == "" {
		v.Owner = owner
	}
	if v.Repo == "" {
		v.Repo = repo
	}
	if err := s.Preferences.Set(p.UserID, v); err != nil {
		return out, err
	}
	v, _ = s.Preferences.Get(p.UserID)
	return SelectionResult{Selection: v, Available: true, Permissions: r.Permissions}, nil
}

// ResolveRead restores the last choice for read-only calls. Mutation tools always
// require explicit owner/repo/branch so another chat cannot retarget a write.
func (s *Service) ResolveRead(ctx context.Context, owner, repo, ref string) (string, string, string, error) {
	if owner == "" && repo == "" {
		selection, err := s.Selection(ctx)
		if err != nil {
			return "", "", "", err
		}
		if !selection.Available {
			return "", "", "", errors.New(selection.Message)
		}
		owner, repo = selection.Selection.Owner, selection.Selection.Repo
		if ref == "" {
			ref = selection.Selection.Branch
		}
	} else {
		if err := github.ValidateRepo(owner, repo); err != nil {
			return "", "", "", err
		}
		if p := identity.From(ctx); p != nil && ref == "" {
			if v, ok := s.Preferences.Get(p.UserID); ok && strings.EqualFold(v.Owner, owner) && strings.EqualFold(v.Repo, repo) {
				ref = v.Branch
			}
		}
	}
	return owner, repo, ref, nil
}

type PickerResult struct {
	SelectionResult
	Repositories github.RepositoryPage `json:"repositories"`
	Branches     github.BranchPage     `json:"branches"`
}

func (s *Service) Picker(ctx context.Context) (PickerResult, error) {
	var out PickerResult
	selection, err := s.Selection(ctx)
	if err != nil {
		return out, err
	}
	out.SelectionResult = selection
	out.Repositories, err = s.GitHub.Repositories(ctx, "", 1, 50)
	if err != nil {
		return out, err
	}
	out.Branches = github.BranchPage{Branches: []github.Branch{}, NextPage: -1}
	if selection.Available {
		v := selection.Selection
		out.Branches, err = s.GitHub.Branches(ctx, v.Owner, v.Repo, "", 1, 50)
	}
	return out, err
}
