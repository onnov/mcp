package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CheckSecrets reports common configuration exposure without preventing the
// owner's chosen in-workspace setup from starting. Normal startup never reads
// the whole project tree. This is an advisory audit, not a secrecy boundary.
func CheckSecrets(root string, secrets ...string) error {
	if err := checkLaunchSecrets(root, secrets...); err != nil {
		fmt.Fprintln(os.Stderr, "pc-mcp: configuration audit warning:", err)
	}
	return nil
}

func checkLaunchSecrets(root string, secrets ...string) error {
	active := [][]byte{}
	for _, secret := range secrets {
		if len(secret) >= 16 {
			active = append(active, []byte(secret))
		}
	}
	if len(active) == 0 {
		return nil
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer opened.Close()
	directories := []string{"."}
	if cwd, err := os.Getwd(); err == nil {
		relative, err := filepath.Rel(root, cwd)
		if err == nil && relative != "." && filepath.IsLocal(relative) {
			directories = append(directories, relative)
		}
	}
	if configured := os.Getenv("PC_MCP_CONFIG"); configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return err
		}
		if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
			absolute = resolved
		}
		relative, err := filepath.Rel(root, absolute)
		if err == nil && (relative == "." || filepath.IsLocal(relative)) {
			return errors.New("PC_MCP_CONFIG is inside the workspace and may be read by project tools and commands")
		}
	}
	names := []string{".env", ".pc-mcp.env", ".pc-mcp.ssh.env", "server.env", "start_pc_mcp.sh", "run_pc_mcp.sh", "paswd_hash.md"}
	for _, directory := range directories {
		for _, name := range names {
			path := filepath.Join(directory, name)
			info, err := opened.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if info.IsDir() {
				continue
			}
			f, err := opened.OpenFile(path, os.O_RDONLY|scanNonblock, 0)
			if err != nil {
				return err
			}
			pinned, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			if !pinned.Mode().IsRegular() {
				f.Close()
				return fmt.Errorf("operator configuration must be a regular file: %s", path)
			}
			if pinned.Size() > 8<<20 {
				f.Close()
				return fmt.Errorf("operator configuration exceeds 8 MiB: %s", path)
			}
			b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
			f.Close()
			if err != nil {
				return err
			}
			if len(b) > 8<<20 {
				return fmt.Errorf("operator configuration grew beyond 8 MiB: %s", path)
			}
			for _, secret := range active {
				if bytes.Contains(b, secret) {
					return fmt.Errorf("control-plane credentials are stored in %s and may be read by project tools and commands", path)
				}
			}
		}
	}
	return nil
}

// AuditWorkspaceSecrets is the explicit full-tree audit. Its work limits may
// fail the audit; they must not make ordinary server startup depend on how
// many source files, assets or unrelated projects the owner has.
func AuditWorkspaceSecrets(root string, secrets ...string) error {
	active := [][]byte{}
	for _, secret := range secrets {
		if len(secret) >= 16 {
			active = append(active, []byte(secret))
		}
	}
	if len(active) == 0 {
		return nil
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer opened.Close()
	entries := 0
	var scanned int64
	return fs.WalkDir(opened.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("credential scan cannot inspect %s: %w", path, err)
		}
		entries++
		if entries > 200000 {
			return errors.New("credential scan exceeds 200000 entries; narrow PC_MCP_ROOT")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "vendor", ".git", ".venv", "venv", "dist", "build":
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Inspect all small files, plus potentially larger operator configuration.
		// Binary/dependency archives are excluded; actual env/scripts remain checked.
		if info.Size() > 1<<20 && filepath.Ext(path) != ".sh" && filepath.Ext(path) != ".env" && filepath.Base(path) != ".env" {
			return nil
		}
		if info.Size() > 8<<20 {
			return fmt.Errorf("oversize configuration file during credential scan: %s", path)
		}
		f, err := opened.OpenFile(path, os.O_RDONLY|scanNonblock, 0)
		if err != nil {
			return err
		}
		pinned, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !pinned.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("configuration changed type during credential scan: %s", path)
		}
		b, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(b) > 8<<20 {
			return fmt.Errorf("configuration grew beyond scan limit: %s", path)
		}
		scanned += int64(len(b))
		if scanned > 512<<20 {
			return errors.New("credential scan exceeds 512 MiB; keep control config outside projects and narrow root")
		}
		for _, secret := range active {
			if bytes.Contains(b, secret) {
				return fmt.Errorf("control-plane secret found inside workspace: %s; move operator configuration outside PC_MCP_ROOT and rotate the exposed secret", path)
			}
		}
		return nil
	})
}
