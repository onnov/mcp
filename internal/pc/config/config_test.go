package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootAndStateValidation(t *testing.T) {
	d := t.TempDir()
	root := filepath.Join(d, "projects")
	os.Mkdir(root, 0700)
	state := filepath.Join(d, "state")
	c, e := Parse([]string{"--root", root, "--state", state, "--transport", "stdio"})
	if e != nil || c.Root != root {
		t.Fatal(c, e)
	}
	if _, e = Parse([]string{"--root", root, "--state", filepath.Join(root, "state"), "--transport", "stdio"}); e == nil {
		t.Fatal("state inside root accepted")
	}
	if _, e = Parse([]string{"--root", "/", "--state", state, "--transport", "stdio"}); e == nil {
		t.Fatal("filesystem root accepted")
	}
	if _, e = Parse([]string{"--root", root, "--state", state, "--transport", "stdio", "--max-seconds", "0"}); e == nil {
		t.Fatal("invalid timeout")
	}
}
