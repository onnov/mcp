package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfinementAndRevisions(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "work")
	os.Mkdir(inside, 0700)
	outside := filepath.Join(base, "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(inside, "escape"))
	r, e := os.OpenRoot(inside)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	for _, p := range []string{"../secret", outside, "escape", ".git/config"} {
		if _, e := Read(r, p); e == nil {
			t.Fatalf("read escape accepted: %s", p)
		}
	}
	if _, e := Write(r, "escape", "oops", "new"); e == nil {
		t.Fatal("symlink write accepted")
	}
	out, e := Write(r, "nested/a.go", "one", "new")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Write(r, "nested/a.go", "two", "new"); e == nil {
		t.Fatal("overwrote existing file as new")
	}
	if _, e = Write(r, "nested/a.go", "two", "stale"); e == nil {
		t.Fatal("stale revision accepted")
	}
	out, e = Write(r, "nested/a.go", "two", out.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = Remove(r, "nested/a.go", "stale"); e == nil {
		t.Fatal("stale delete accepted")
	}
	if e = Remove(r, "nested/a.go", out.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(r, "nested/a.go"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "secret" {
		t.Fatal("outside file changed")
	}
}
func TestReadBoundAndDirectoryPagination(t *testing.T) {
	dir := t.TempDir()
	r, _ := os.OpenRoot(dir)
	defer r.Close()
	os.WriteFile(filepath.Join(dir, "large"), []byte(strings.Repeat("x", MaxFileBytes+1)), 0600)
	if _, e := Read(r, "large"); e == nil {
		t.Fatal("oversize file accepted")
	}
	for _, name := range []string{"b", "a", "c"} {
		os.Mkdir(filepath.Join(dir, name), 0700)
	}
	p, e := List(r, ".", 0, 2, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Entries) != 2 || p.Entries[0].Name != "a" || p.NextOffset != 2 {
		t.Fatalf("unexpected page %+v", p)
	}
	if _, e = List(r, ".", -1, 100, ""); e == nil {
		t.Fatal("negative offset accepted")
	}
}
