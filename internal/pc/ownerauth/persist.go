package ownerauth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Grants survive restarts as one file per connection:
// <StateDir>/<client>/<grant-id>.json, client is chatgpt or claude. Files hold
// only SHA-256 hashes of tokens, never usable tokens. Deleting a file or a
// client directory revokes those connections immediately, even while running.

var grantIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var persistedClients = []string{"chatgpt", "claude"}

const maxUsedRefresh = 32

type grantFile struct {
	Expires       time.Time `json:"expires"`
	Access        string    `json:"access_sha256"`
	AccessExpires time.Time `json:"access_expires"`
	Refresh       string    `json:"refresh_sha256"`
	UsedRefresh   []string  `json:"used_refresh_sha256,omitempty"`
}

// clientKey names the per-client grant directory by the validated callback.
func clientKey(redirectURI string) string {
	if u, err := url.Parse(redirectURI); err == nil && u.Host == "chatgpt.com" {
		return "chatgpt"
	}
	return "claude"
}

func newGrantID() string { return hex.EncodeToString(randomBytes()[:16]) }

func (s *Server) grantPath(g *grant) string {
	return filepath.Join(s.StateDir, g.Client, g.ID+".json")
}

// saveGrant atomically writes the grant's current token hashes.
func (s *Server) saveGrant(g *grant, access [32]byte, accessExpires time.Time, refresh [32]byte, used [][32]byte) error {
	if s.StateDir == "" {
		return nil
	}
	f := grantFile{Expires: g.Expires, Access: hex.EncodeToString(access[:]), AccessExpires: accessExpires, Refresh: hex.EncodeToString(refresh[:])}
	for _, u := range used {
		f.UsedRefresh = append(f.UsedRefresh, hex.EncodeToString(u[:]))
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.grantPath(g))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".grant-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(b); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), s.grantPath(g))
}

func (s *Server) deleteGrantFile(g *grant) {
	if s.StateDir != "" {
		_ = os.Remove(s.grantPath(g))
	}
}

// grantAlive reports whether the owner kept the grant's file. A missing file
// revokes the grant in memory too.
func (s *Server) grantAlive(g *grant) bool {
	if s.StateDir == "" {
		return true
	}
	_, err := os.Stat(s.grantPath(g))
	if errors.Is(err, os.ErrNotExist) {
		s.revokeGrant(g)
	}
	return err == nil
}

func decodeHash(raw string) ([32]byte, bool) {
	var h [32]byte
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != len(h) {
		return h, false
	}
	copy(h[:], b)
	return h, true
}

// loadGrants restores unexpired grants and removes expired or invalid files.
func (s *Server) loadGrants() error {
	if s.StateDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.StateDir, 0700); err != nil {
		return err
	}
	now := time.Now()
	for _, client := range persistedClients {
		entries, err := os.ReadDir(filepath.Join(s.StateDir, client))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !e.Type().IsRegular() || !grantIDPattern.MatchString(id) {
				continue
			}
			path := filepath.Join(s.StateDir, client, e.Name())
			var f grantFile
			b, err := os.ReadFile(path)
			if err != nil || len(b) > 64<<10 || json.Unmarshal(b, &f) != nil || !now.Before(f.Expires) || len(s.refresh) >= 100 {
				_ = os.Remove(path)
				continue
			}
			refresh, ok := decodeHash(f.Refresh)
			if !ok {
				_ = os.Remove(path)
				continue
			}
			g := &grant{ID: id, Client: client, Expires: f.Expires}
			s.refresh[refresh] = g
			if accessHash, ok := decodeHash(f.Access); ok && now.Before(f.AccessExpires) {
				s.access[accessHash] = access{Grant: g, Expires: f.AccessExpires}
			}
			for _, raw := range f.UsedRefresh {
				if used, ok := decodeHash(raw); ok && len(s.usedRefresh) < 1024 {
					s.usedRefresh[used] = g
					g.used = append(g.used, used)
				}
			}
		}
	}
	return nil
}
