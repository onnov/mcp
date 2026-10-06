//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package app

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("exclusive state locking is not implemented on this OS")
}
