// Package preferences persists the last repository and branch per immutable user ID,
// and each chat's own repository and branch. It intentionally never stores
// OAuth credentials.
package preferences

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Selection struct {
	Owner        string    `json:"owner"`
	Repo         string    `json:"repo"`
	Branch       string    `json:"branch"`
	RepositoryID int64     `json:"repository_id"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	users map[string]profile
}

type profile struct {
	Last         Selection            `json:"last"`
	Repositories map[string]Selection `json:"repositories"`
	// Chats binds each chat (by SessionKey) to its own repository and branch.
	Chats map[string]Selection `json:"chats,omitempty"`
}

const (
	version         = 3
	maxFileSize     = 8 << 20
	maxRepositories = 200
	// maxChats bounds bindings per user; the least recently used is dropped.
	maxChats = 500
	// chatTTL drops bindings of chats unused for this long.
	chatTTL = 180 * 24 * time.Hour
)

// SessionKey hashes a chat session identifier (ChatGPT openai/session or
// "chat:<key>"), so raw chat identifiers are never written to disk.
func SessionKey(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:16])
}

type document struct {
	Version int                `json:"version"`
	Users   map[string]profile `json:"users"`
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "selections.json"), users: map[string]profile{}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, errors.New("preferences file exceeds 8 MiB")
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid preferences file: %w", err)
	}
	// Version 2 has no chat bindings; version 3 adds them. Both load as is and
	// the next write saves version 3.
	if doc.Version == 2 || doc.Version == version {
		s.users = doc.Users
	} else if doc.Version == 0 {
		var old map[string]Selection
		if err := json.Unmarshal(data, &old); err != nil {
			return nil, err
		}
		for userID, v := range old {
			s.users[userID] = profile{Last: v, Repositories: map[string]Selection{strconv.FormatInt(v.RepositoryID, 10): v}}
		}
	} else {
		return nil, errors.New("unsupported preferences version")
	}
	if s.users == nil {
		s.users = map[string]profile{}
	}
	return s, nil
}

func (s *Store) Get(userID int64) (Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.users[strconv.FormatInt(userID, 10)]
	return v.Last, ok
}

func (s *Store) Repository(userID, repositoryID int64) (Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.users[strconv.FormatInt(userID, 10)]
	v, ok := p.Repositories[strconv.FormatInt(repositoryID, 10)]
	return v, ok
}

// Chat returns the repository and branch bound to one chat of the user.
func (s *Store) Chat(userID int64, sessionKey string) (Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.users[strconv.FormatInt(userID, 10)].Chats[sessionKey]
	return v, ok
}

// Set saves the user's last choice, used by new chats and by calls without a chat.
func (s *Store) Set(userID int64, value Selection) error {
	return s.update(userID, func(p *profile) bool {
		p.setLast(value)
		return true
	})
}

// SetChat binds one chat to a choice and makes it the user's last choice, so
// new chats start from it. Other chats keep their bindings.
func (s *Store) SetChat(userID int64, sessionKey string, value Selection) error {
	return s.update(userID, func(p *profile) bool {
		p.setChat(sessionKey, p.setLast(value))
		return true
	})
}

// BindChat binds a chat that has no binding yet to the user's last choice.
// It reports whether a binding was created.
func (s *Store) BindChat(userID int64, sessionKey string) (Selection, bool, error) {
	created := false
	var bound Selection
	err := s.update(userID, func(p *profile) bool {
		if v, ok := p.Chats[sessionKey]; ok {
			bound = v
			return false
		}
		if p.Last.Repo == "" {
			return false
		}
		bound, created = p.Last, true
		p.setChat(sessionKey, p.Last)
		return true
	})
	return bound, created && err == nil, err
}

func (p *profile) setLast(value Selection) Selection {
	value.UpdatedAt = time.Now().UTC()
	repositories := map[string]Selection{}
	for k, v := range p.Repositories {
		repositories[k] = v
	}
	repoKey := strconv.FormatInt(value.RepositoryID, 10)
	if _, exists := repositories[repoKey]; !exists && len(repositories) >= maxRepositories {
		delete(repositories, oldest(repositories))
	}
	repositories[repoKey] = value
	p.Last, p.Repositories = value, repositories
	return value
}

func (p *profile) setChat(sessionKey string, value Selection) {
	chats := map[string]Selection{}
	cutoff := time.Now().Add(-chatTTL)
	for k, v := range p.Chats {
		if v.UpdatedAt.After(cutoff) {
			chats[k] = v
		}
	}
	if _, exists := chats[sessionKey]; !exists && len(chats) >= maxChats {
		delete(chats, oldest(chats))
	}
	value.UpdatedAt = time.Now().UTC()
	chats[sessionKey] = value
	p.Chats = chats
}

func oldest(m map[string]Selection) string {
	key := ""
	var when time.Time
	for k, v := range m {
		if key == "" || v.UpdatedAt.Before(when) {
			key, when = k, v.UpdatedAt
		}
	}
	return key
}

// update applies change to a copy of the user's profile and publishes it in
// memory only after the file was replaced atomically.
// change reports whether it modified the profile; unchanged profiles are not written.
func (s *Store) update(userID int64, change func(*profile) bool) error {
	if userID <= 0 {
		return errors.New("invalid user ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatInt(userID, 10)
	if _, exists := s.users[key]; !exists && len(s.users) >= 10000 {
		return errors.New("preferences capacity reached")
	}
	previous := s.users[key]
	p := previous // setLast and setChat replace the maps instead of mutating them.
	if !change(&p) {
		return nil
	}
	next := make(map[string]profile, len(s.users)+1)
	for k, v := range s.users {
		next[k] = v
	}
	next[key] = p
	data, err := json.MarshalIndent(document{Version: version, Users: next}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxFileSize {
		return errors.New("preferences file exceeds 8 MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".selections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	s.users = next // Publish only after a successful atomic replacement.
	return nil
}
