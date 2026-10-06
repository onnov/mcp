// Package files provides bounded, traversal-resistant access with os.Root.
package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxFileBytes = 512 * 1024

func Valid(path string) error {
	if path == "" || filepath.IsAbs(path) || !filepath.IsLocal(path) {
		return errors.New("use a relative path inside the workspace")
	}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool { return r == '/' }) {
		if strings.EqualFold(part, ".git") {
			return errors.New(".git metadata is not exposed by file tools")
		}
	}
	return nil
}
func Revision(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Symlink   bool   `json:"symlink"`
}
type Page struct {
	Entries    []Entry `json:"entries"`
	NextOffset int     `json:"next_offset"`
}

func List(root *os.Root, path string, offset, limit int, query string) (Page, error) {
	out := Page{Entries: []Entry{}, NextOffset: -1}
	if e := Valid(path); e != nil {
		return out, e
	}
	if offset < 0 {
		return out, errors.New("negative offset")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	f, e := root.OpenFile(path, os.O_RDONLY|nonblock, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.IsDir() {
		return out, errors.New("not a directory")
	}
	// Read at most 10,001 directory entries; bound RAM even for generated directories.
	rows, e := f.ReadDir(10001)
	if e != nil && e != io.EOF {
		return out, e
	}
	if len(rows) > 10000 {
		return out, errors.New("directory exceeds 10000 entries; choose a narrower directory")
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsDir() != rows[j].IsDir() {
			return rows[i].IsDir()
		}
		return rows[i].Name() < rows[j].Name()
	})
	matches := []Entry{}
	for _, r := range rows {
		if strings.EqualFold(r.Name(), ".git") || !strings.Contains(strings.ToLower(r.Name()), strings.ToLower(query)) {
			continue
		}
		matches = append(matches, Entry{r.Name(), filepath.ToSlash(filepath.Join(path, r.Name())), r.IsDir(), r.Type()&os.ModeSymlink != 0})
	}
	if offset >= len(matches) {
		return out, nil
	}
	end := min(offset+limit, len(matches))
	out.Entries = matches[offset:end]
	if end < len(matches) {
		out.NextOffset = end
	}
	return out, nil
}

type File struct {
	Path     string `json:"path"`
	Text     string `json:"text"`
	Revision string `json:"revision"`
}

func Read(root *os.Root, path string) (File, error) {
	out := File{Path: path}
	if e := Valid(path); e != nil {
		return out, e
	}
	f, e := root.OpenFile(path, os.O_RDONLY|nonblock, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() {
		return out, errors.New("only regular files are supported")
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if e != nil {
		return out, e
	}
	if len(b) > MaxFileBytes {
		return out, errors.New("file exceeds 512 KiB")
	}
	out.Text = string(b)
	out.Revision = Revision(b)
	return out, nil
}

// Write checks a revision and uses exclusive staging plus Root.Rename. Callers
// serialize mutations; external editors may still race, as with normal git tools.
func Write(root *os.Root, path, text, expected string) (File, error) {
	if e := Valid(path); e != nil {
		return File{}, e
	}
	if len(text) > MaxFileBytes {
		return File{}, errors.New("file exceeds 512 KiB")
	}
	if expected == "" {
		return File{}, errors.New("expected_revision is required; use new for a new file")
	}
	mode := os.FileMode(0644)
	old, e := Read(root, path)
	if expected == "new" {
		if e == nil {
			return File{}, errors.New("file already exists")
		}
		if !errors.Is(e, os.ErrNotExist) {
			return File{}, e
		}
	} else {
		if e != nil {
			return File{}, e
		}
		if old.Revision != expected {
			return File{}, errors.New("revision conflict; read the file again")
		}
		s, e := root.Lstat(path)
		if e != nil {
			return File{}, e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return File{}, errors.New("cannot replace a symlink")
		}
		mode = s.Mode().Perm()
	}
	if e = root.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return File{}, e
	}
	// A random name supplied by crypto/rand is created inside the rooted directory.
	for i := 0; i < 8; i++ {
		name := filepath.Join(filepath.Dir(path), ".pc-mcp-"+randomID())
		f, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(e, os.ErrExist) {
			continue
		}
		if e != nil {
			return File{}, e
		}
		_, e = f.WriteString(text)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			if expected == "new" {
				// Hard-link publication is atomic and never replaces a concurrently created file.
				e = root.Link(name, path)
				_ = root.Remove(name)
			} else {
				current, checkErr := Read(root, path)
				if checkErr != nil {
					e = checkErr
				} else if current.Revision != expected {
					e = errors.New("revision conflict; read the file again")
				} else {
					e = root.Rename(name, path)
				}
			}
		}
		if e != nil {
			_ = root.Remove(name)
			return File{}, e
		}
		return File{path, text, Revision([]byte(text))}, nil
	}
	return File{}, errors.New("could not stage file")
}
func Remove(root *os.Root, path, expected string) error {
	old, e := Read(root, path)
	if e != nil {
		return e
	}
	if old.Revision != expected {
		return errors.New("revision conflict")
	}
	return root.Remove(path)
}
