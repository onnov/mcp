package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIgnoredControlSecretsAreRejectedWithoutDisclosure(t *testing.T) {
	root := t.TempDir()
	secret := strings.Repeat("sensitive", 8)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("start_pc_mcp.sh\n"), 0600)
	os.WriteFile(filepath.Join(root, "start_pc_mcp.sh"), []byte("export TOKEN='"+secret+"'"), 0600)
	err := checkLaunchSecrets(root, secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("secret scan missed credential or disclosed it", err)
	}
	if err = CheckSecrets(root, secret); err != nil {
		t.Fatal("owner's in-workspace config prevented startup", err)
	}
	os.Remove(filepath.Join(root, "start_pc_mcp.sh"))
	if err = checkLaunchSecrets(root, secret); err != nil {
		t.Fatal(err)
	}
}

func TestStartupDoesNotScanLargeWorkspace(t *testing.T) {
	root := t.TempDir()
	// Sparse source files exceed the old 512 MiB startup budget without needing
	// to write hundreds of MiB of fixture contents.
	for i := 0; i < 513; i++ {
		f, err := os.Create(filepath.Join(root, fmt.Sprintf("source-%04d.txt", i)))
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(1 << 20)
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if err := CheckSecrets(root, strings.Repeat("sensitive", 8)); err != nil {
		t.Fatal("project size blocks startup", err)
	}
}

func TestStartupChecksLaunchDirectory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "AI", "mcp")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	secret := strings.Repeat("sensitive", 8)
	if err := os.WriteFile(filepath.Join(project, "start_pc_mcp.sh"), []byte("export TOKEN='"+secret+"'"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkLaunchSecrets(root, secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("launch configuration secret missed or disclosed", err)
	}
	if err := CheckSecrets(root, secret); err != nil {
		t.Fatal("launch config warning became startup failure", err)
	}
}

func TestFullAuditRemainsExplicit(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("sensitive", 8)
	if err := os.WriteFile(filepath.Join(project, "settings.txt"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSecrets(root, secret); err != nil {
		t.Fatal("startup recursed into unrelated project", err)
	}
	if err := AuditWorkspaceSecrets(root, secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("explicit audit missed or disclosed secret", err)
	}
}
