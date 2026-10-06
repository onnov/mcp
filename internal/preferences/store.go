// Package preferences persists the last repository and branch per immutable user ID.
// It intentionally never stores OAuth credentials.
package preferences

import (
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
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("preferences file exceeds 2 MiB")
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid preferences file: %w", err)
	}
	if doc.Version == 2 {
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

func (s *Store) Set(userID int64, value Selection) error {
	if userID <= 0 {
		return errors.New("invalid user ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatInt(userID, 10)
	if _, exists := s.users[key]; !exists && len(s.users) >= 10000 {
		return errors.New("preferences capacity reached")
	}
	next := make(map[string]profile, len(s.users)+1)
	for k, v := range s.users {
		next[k] = v
	}
	value.UpdatedAt = time.Now().UTC()
	previous := s.users[key]
	repositories := map[string]Selection{}
	for k, v := range previous.Repositories {
		repositories[k] = v
	}
	repoKey := strconv.FormatInt(value.RepositoryID, 10)
	if _, exists := repositories[repoKey]; !exists && len(repositories) >= 200 {
		oldest := ""
		var when time.Time
		for k, v := range repositories {
			if oldest == "" || v.UpdatedAt.Before(when) {
				oldest = k
				when = v.UpdatedAt
			}
		}
		delete(repositories, oldest)
	}
	repositories[repoKey] = value
	next[key] = profile{Last: value, Repositories: repositories}
	data, err := json.MarshalIndent(document{Version: 2, Users: next}, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("preferences file exceeds 2 MiB")
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
