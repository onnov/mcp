package files

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestConcurrentCreatePublishesOneWinner(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	start := make(chan struct{})
	results := make(chan File, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			file, err := Write(root, "new.txt", fmt.Sprint(i), "new")
			if err == nil {
				results <- file
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := []File{}
	for file := range results {
		winners = append(winners, file)
	}
	actual, err := Read(root, "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(winners) != 1 || actual.Revision != winners[0].Revision {
		t.Fatal("concurrent create replaced a winner", len(winners), actual)
	}
}

func TestGitMetadataCaseVariants(t *testing.T) {
	for _, path := range []string{".GIT/config", "project/.Git/HEAD", ".git/objects/x"} {
		if Valid(path) == nil {
			t.Fatal("metadata case variant accepted", path)
		}
	}
}
