package preferences

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistenceUserIsolationAndBranchHistory(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		user      int64
		selection Selection
	}{{1, Selection{Owner: "a", Repo: "one", Branch: "feature/a", RepositoryID: 10}}, {2, Selection{Owner: "b", Repo: "two", Branch: "main", RepositoryID: 20}}, {1, Selection{Owner: "a", Repo: "other", Branch: "work", RepositoryID: 11}}} {
		if e := s.Set(v.user, v.selection); e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if v, _ := reopened.Get(1); v.Repo != "other" {
		t.Fatalf("wrong last choice: %+v", v)
	}
	if v, _ := reopened.Repository(1, 10); v.Branch != "feature/a" {
		t.Fatalf("branch history lost: %+v", v)
	}
	if v, _ := reopened.Get(2); v.Owner != "b" {
		t.Fatalf("cross-user leak: %+v", v)
	}
	if _, ok := reopened.Get(3); ok {
		t.Fatal("unknown user has a selection")
	}
	if st, e := os.Stat(filepath.Join(dir, "selections.json")); e != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", st, e)
	}
}

func TestFailedWriteDoesNotPublishPreference(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	v := Selection{Owner: "a", Repo: "b", Branch: "main", RepositoryID: 1}
	if e = s.Set(1, v); e != nil {
		t.Fatal(e)
	}
	s.path = filepath.Join(t.TempDir(), "absent", "selections.json")
	v.Branch = "other"
	if e = s.Set(1, v); e == nil {
		t.Fatal("expected write failure")
	}
	if saved, _ := s.Get(1); saved.Branch != "main" {
		t.Fatal("failed write changed memory")
	}
}

func TestConcurrentSelections(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := int64(1); i <= 10; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if e := s.Set(id, Selection{Owner: "a", Repo: "b", Branch: "main", RepositoryID: 1}); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	for i := int64(1); i <= 10; i++ {
		if _, ok := s.Get(i); !ok {
			t.Fatalf("lost user %d", i)
		}
	}
}

func TestCorruptFileIsNotSilentlyReset(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "selections.json"), []byte("{broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(dir); e == nil {
		t.Fatal("corrupt preferences accepted")
	}
}
