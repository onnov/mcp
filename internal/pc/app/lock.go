package app

import (
	"fmt"
	"os"
	"path/filepath"
)

func lockState(state string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(state, "instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lockFile(f); e != nil {
		f.Close()
		return nil, fmt.Errorf("state is already in use or cannot be locked: %w", e)
	}
	return f, nil
}
