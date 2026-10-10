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
	// SessionBound reports that the selection is this chat's own binding.
	SessionBound bool `json:"session_bound"`
	// Chat is the server-issued chat key for clients without chat metadata.
	Chat string `json:"chat,omitempty"`
}

// Selection restores the user's last choice, used by calls without a chat.
func (s *Service) Selection(ctx context.Context) (SelectionResult, error) {
	return s.SessionSelection(ctx, "")
}

// SessionSelection restores this chat's repository and branch. Without a chat
// session it restores the user's last choice.
func (s *Service) SessionSelection(ctx context.Context, session string) (SelectionResult, error) {
	var out SelectionResult
	p, err := identity.Require(ctx)
	if err != nil {
		return out, err
	}
	v, ok := s.Preferences.Get(p.UserID)
	if session != "" {
		v, ok = s.Preferences.Chat(p.UserID, preferences.SessionKey(session))
		out.SessionBound = ok
	}
	if !ok {
		out.Message = "Select a repository to start"
		return out, nil
	}
	return s.check(ctx, v, out)
}

// check confirms that a saved choice is still accessible. It never changes
// preferences: a missing branch falls back to main/default for this answer only.
func (s *Service) check(ctx context.Context, v preferences.Selection, out SelectionResult) (SelectionResult, error) {
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

// BindDefault binds a chat that has no repository yet to the user's last
// choice, so every chat works in a definite repository and branch from its
// first call. It reports whether it created the binding.
func (s *Service) BindDefault(ctx context.Context, session string) (bool, error) {
	p := identity.From(ctx)
	if session == "" || p == nil || p.UserID <= 0 {
		return false, nil
	}
	_, created, err := s.Preferences.BindChat(p.UserID, preferences.SessionKey(session))
	return created, err
}

// Select chooses a repository and branch. With a chat session it rebinds only
// that chat; the choice also becomes the user's last choice for new chats.
func (s *Service) Select(ctx context.Context, session, owner, repo, branch string) (SelectionResult, error) {
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
	if session == "" {
		err = s.Preferences.Set(p.UserID, v)
	} else {
		err = s.Preferences.SetChat(p.UserID, preferences.SessionKey(session), v)
	}
	if err != nil {
		return out, err
	}
	v, _ = s.Preferences.Get(p.UserID)
	return SelectionResult{Selection: v, Available: true, Permissions: r.Permissions, SessionBound: session != ""}, nil
}

// ResolveRead restores this chat's choice (or the last choice without a chat)
// for read-only calls. Mutation tools always require explicit owner/repo/branch
// so a stale or foreign selection cannot retarget a write.
func (s *Service) ResolveRead(ctx context.Context, session, owner, repo, ref string) (string, string, string, error) {
	if owner == "" && repo == "" {
		selection, err := s.SessionSelection(ctx, session)
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
			v, ok := s.Preferences.Get(p.UserID)
			if session != "" {
				v, ok = s.Preferences.Chat(p.UserID, preferences.SessionKey(session))
			}
			if ok && strings.EqualFold(v.Owner, owner) && strings.EqualFold(v.Repo, repo) {
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

func (s *Service) Picker(ctx context.Context, session string) (PickerResult, error) {
	var out PickerResult
	selection, err := s.SessionSelection(ctx, session)
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
